#include <stdlib.h>
#include <atomic>
#include <new>
#include <string>
#include <string.h>

#include "Luau/Allocator.h"
#include "Luau/Lexer.h"
#include "Luau/Parser.h"
#include "bridge.h"
#include "lua.h"
#include "lualib.h"
#include "luacode.h"

extern "C" int goCallback(lua_State* L, int id);  // implemented in Go

#define GO_UDATA_TAG 1

extern "C" void goUserdataFinalizer(uintptr_t handle);  // implemented in Go

// Runs when Luau collects one of our userdata (or at lua_close).
// It must not throw and must not call back into Luau.
static void go_udata_dtor(lua_State* L, void* data) {
    goUserdataFinalizer(*(uintptr_t*)data);
}

// Every Go function is exposed to Luau through this one trampoline.
// The function's registry ID is stored as upvalue 1.
//
// goCallback returns:   n >= 0  -> return n results
//                       -1      -> raise the error value on top of the stack
//                       n <= -2 -> yield (-n - 2) values
static int trampoline(lua_State* L) {
    int id = (int)lua_tonumber(L, lua_upvalueindex(1));
    int n = goCallback(L, id);
    if (n == -1) {
        // Go has already returned, so throwing only unwinds C++ frames.
        if (lua_gettop(L) == 0) lua_pushstring(L, "go function failed");
        lua_error(L);
    }
    if (n <= -2) return lua_yield(L, -n - 2);   // no exception involved
    return n;
}

// Per-state host data. It is the allocator's `ud`, and also lives in
// lua_callbacks(L)->userdata, so callbacks can find it from any thread.
struct Host {
    uintptr_t handle;
    size_t used;
    size_t limit;
    std::atomic<int> interrupted;
    std::string traceback;       // traceback of the last error, filled by pcall_handler
};

static Host* host_of(lua_State* L) { return (Host*)lua_callbacks(L)->userdata; }

// Makes room for n more stack slots before something is pushed. Luau does
// not check this in release builds, so every function below that pushes
// calls it first. Returns 0 on success and -1 if the stack can't grow.
static int reserve(lua_State* L, int n) {
    try { return lua_checkstack(L, n) ? 0 : -1; }
    catch (...) { return -1; }
}

// Appends one line per stack frame, innermost first, starting at firstlevel.
// It pushes nothing on the Lua stack, so it is safe on a dead coroutine.
static void build_trace(lua_State* L, int firstlevel, std::string& out) {
    lua_Debug ar;
    for (int level = firstlevel; lua_getinfo(L, level, "sln", &ar); ++level) {
        if (level - firstlevel >= 50) { out += "\n..."; break; }
        if (!out.empty()) out += "\n";
        out += ar.short_src ? ar.short_src : "?";
        if (ar.currentline > 0) { out += ":"; out += std::to_string(ar.currentline); }
        if (ar.name) { out += " function "; out += ar.name; }
    }
}

// Error handler for lua_pcall. Luau calls it with the error value before
// the stack is unwound, so the traceback shows where the error happened.
// It returns the error value unchanged.
static int pcall_handler(lua_State* L) {
    Host* h = host_of(L);
    try {
        h->traceback.clear();
        build_trace(L, 1, h->traceback);   // level 0 is this handler
    } catch (const std::bad_alloc&) {
        h->traceback.clear();
    }
    lua_pushvalue(L, 1);
    return 1;
}

// Allocator with usage tracking and an optional limit. Returning NULL makes
// Luau raise an out-of-memory error, which pcall catches.
static void* host_alloc(void* ud, void* ptr, size_t osize, size_t nsize) {
    Host* h = (Host*)ud;
    size_t old = ptr ? osize : 0;   // for new blocks Luau's osize is not a size
    size_t base = h->used >= old ? h->used - old : 0;

    if (nsize == 0) {
        free(ptr);
        h->used = base;
        return nullptr;
    }
    if (h->limit && nsize > old && base + nsize > h->limit) return nullptr;

    void* p = realloc(ptr, nsize);
    if (!p) return nullptr;
    h->used = base + nsize;
    return p;
}

// Runs at Luau safepoints (loop back edges, calls, returns, GC steps).
// While the flag is set it keeps raising an error at every safepoint, so a
// script can't simply catch the error with pcall and carry on.
static void interrupt_cb(lua_State* L, int gc) {
    if (gc >= 0) return;   // inside a GC step: never raise an error here
    if (host_of(L)->interrupted.load(std::memory_order_relaxed))
        luaL_error(L, "script interrupted");
}

struct LibEntry { unsigned flag; const char* name; int (*fn)(lua_State*); };
static const LibEntry kLibs[] = {
    { BRIDGE_LIB_BASE,      "",          luaopen_base },
    { BRIDGE_LIB_COROUTINE, "coroutine", luaopen_coroutine },
    { BRIDGE_LIB_TABLE,     "table",     luaopen_table },
    { BRIDGE_LIB_OS,        "os",        luaopen_os },
    { BRIDGE_LIB_STRING,    "string",    luaopen_string },
    { BRIDGE_LIB_BIT32,     "bit32",     luaopen_bit32 },
    { BRIDGE_LIB_BUFFER,    "buffer",    luaopen_buffer },
    { BRIDGE_LIB_UTF8,      "utf8",      luaopen_utf8 },
    { BRIDGE_LIB_CLASS,     "class",     luaopen_class },
    { BRIDGE_LIB_MATH,      "math",      luaopen_math },
    { BRIDGE_LIB_DEBUG,     "debug",     luaopen_debug },
    { BRIDGE_LIB_VECTOR,    "vector",    luaopen_vector },
    { BRIDGE_LIB_INTEGER,   "integer",   luaopen_integer },
};

static int open_libs(lua_State* L, unsigned libs) {
    try {
        if (libs == 0) { luaL_openlibs(L); return 0; }   // the default set
        for (const LibEntry& e : kLibs) {
            if (libs & e.flag) {
                lua_pushcfunction(L, e.fn, nullptr);
                lua_pushstring(L, e.name);
                lua_call(L, 1, 0);
            }
        }
        return 0;
    } catch (...) { return -1; }
}

extern "C" {
    lua_State* bridge_newstate(uintptr_t handle, unsigned int libs, size_t memlimit) {
        Host* h = new (std::nothrow) Host();
        if (!h) return nullptr;
        h->handle = handle;
        h->used = 0;
        h->limit = memlimit;
        h->interrupted = 0;

        lua_State* L = nullptr;
        try { L = lua_newstate(host_alloc, h); } catch (...) {}
        if (!L) { delete h; return nullptr; }

        lua_callbacks(L)->userdata = h;
        lua_callbacks(L)->interrupt = interrupt_cb;
        lua_setuserdatadtor(L, GO_UDATA_TAG, go_udata_dtor);

        if (open_libs(L, libs) != 0) { bridge_close(L); return nullptr; }
        return L;
    }

    void bridge_close(lua_State* L) {
        void* ud = nullptr;
        lua_getallocf(L, &ud);   // fetch the Host before the state goes away
        lua_close(L);            // frees every object through host_alloc
        delete (Host*)ud;        // so the Host must outlive lua_close
    }

    uintptr_t bridge_getuserdata(lua_State* L) { return host_of(L)->handle; }

    size_t bridge_memused(lua_State* L) { return host_of(L)->used; }
    int bridge_errmem(void) { return LUA_ERRMEM; }

    void bridge_interrupt(lua_State* L) { host_of(L)->interrupted.store(1); }
    void bridge_clearinterrupt(lua_State* L) { host_of(L)->interrupted.store(0); }
    int bridge_isinterrupted(lua_State* L) { return host_of(L)->interrupted.load(); }

    void bridge_sandbox(lua_State* L) {
        try { luaL_sandbox(L); } catch (...) {}
    }

    // Pushes a Luau function that calls Go function `id`.
    // NOTE: Luau does not copy `name`, so it must stay valid for the state's lifetime.
    int bridge_pushgofunction(lua_State* L, int id, const char* name) {
        try {
            lua_pushnumber(L, id);
            lua_pushcclosurek(L, trampoline, name, 1, nullptr);
            return 0;
        } catch (...) { return -1; }
    }

    char* bridge_compile(const char* src, size_t len, size_t* outlen) {
        try {
            return luau_compile(src, len, nullptr, outlen);
        } catch (...) {
            *outlen = 0;
            return nullptr;
        }
    }

    int bridge_load(lua_State* L, const char* chunkname, const char* data, size_t size) {
        host_of(L)->traceback.clear();
        try {
            return luau_load(L, chunkname, data, size, 0);
        } catch (...) {
            lua_pushstring(L, "internal error");
            return LUA_ERRMEM;
        }
    }

    int bridge_pcall(lua_State* L, int nargs, int nresults) {
        host_of(L)->traceback.clear();
        try {
            int funcidx = lua_gettop(L) - nargs;
            lua_pushcfunction(L, pcall_handler, "traceback");
            lua_insert(L, funcidx);                        // handler goes below the function
            int status = lua_pcall(L, nargs, nresults, funcidx);
            lua_remove(L, funcidx);                        // results (or the error) stay
            return status;
        } catch (...) {
            lua_pushstring(L, "internal error");
            return LUA_ERRRUN;
        }
    }

    int bridge_parseerror(const char* src, size_t len, int* line, int* col, char** msg) {
        try {
            Luau::Allocator allocator;
            Luau::AstNameTable names(allocator);
            Luau::ParseResult result = Luau::Parser::parse(src, len, names, allocator, Luau::ParseOptions());
            if (result.errors.empty()) return 0;

            const Luau::ParseError& e = result.errors.front();
            *line = (int)e.getLocation().begin.line + 1;      // Luau counts from 0
            *col = (int)e.getLocation().begin.column + 1;
            const char* text = e.what();
            size_t n = strlen(text);
            *msg = (char*)malloc(n + 1);
            if (!*msg) return 0;
            memcpy(*msg, text, n + 1);
            return 1;
        } catch (...) {
            return 0;
        }
    }

    const char* bridge_traceback(lua_State* L, size_t* len) {
        Host* h = host_of(L);
        *len = h->traceback.size();
        return h->traceback.c_str();
    }

    void bridge_cleartraceback(lua_State* L) { host_of(L)->traceback.clear(); }

    void bridge_tracethread(lua_State* co) {
        Host* h = host_of(co);
        try {
            h->traceback.clear();
            build_trace(co, 0, h->traceback);
        } catch (const std::bad_alloc&) {
            h->traceback.clear();
        }
    }

    int bridge_checkstack(lua_State* L, int n) {
        try { return lua_checkstack(L, n); }
        catch (...) { return 0; }
    }

    const char* bridge_tostring(lua_State* L, int idx, size_t* len) {
        return lua_tolstring(L, idx, len);
    }

    void bridge_settop(lua_State* L, int idx) { lua_settop(L, idx); }

    int bridge_gettop(lua_State* L) { return lua_gettop(L); }
    int bridge_type(lua_State* L, int idx) { return lua_type(L, idx); }
    const char* bridge_typename(lua_State* L, int idx) {
        return lua_typename(L, lua_type(L, idx));
    }

    int bridge_pushnil(lua_State* L) { if (reserve(L, 1)) return -1; lua_pushnil(L); return 0; }
    int bridge_pushboolean(lua_State* L, int b) { if (reserve(L, 1)) return -1; lua_pushboolean(L, b); return 0; }
    int bridge_pushnumber(lua_State* L, double n) { if (reserve(L, 1)) return -1; lua_pushnumber(L, n); return 0; }
    int bridge_pushlstring(lua_State* L, const char* s, size_t len) {
        try {
            lua_pushlstring(L, len ? s : "", len);
            return 0;
        } catch (...) { return -1; }
    }

    int bridge_toboolean(lua_State* L, int idx) { return lua_toboolean(L, idx); }
    double bridge_tonumber(lua_State* L, int idx, int* ok) {
        return lua_tonumberx(L, idx, ok);
    }

    int bridge_getglobal(lua_State* L, const char* name) {
        try { lua_getglobal(L, name); return 0; }
        catch (...) { return -1; }
    }
    int bridge_setglobal(lua_State* L, const char* name) {
        try { lua_setglobal(L, name); return 0; }
        catch (...) { return -1; }
    }

    int bridge_createtable(lua_State* L, int narr, int nrec) {
        try { lua_createtable(L, narr, nrec); return 0; } catch (...) { return -1; }
    }
    int bridge_rawgetfield(lua_State* L, int idx, const char* k) {
        try { lua_rawgetfield(L, idx, k); return 0; } catch (...) { return -1; }
    }
    int bridge_rawsetfield(lua_State* L, int idx, const char* k) {
        try { lua_rawsetfield(L, idx, k); return 0; } catch (...) { return -1; }
    }
    int bridge_rawgeti(lua_State* L, int idx, int n) {
        try { lua_rawgeti(L, idx, n); return 0; } catch (...) { return -1; }
    }
    int bridge_rawseti(lua_State* L, int idx, int n) {
        try { lua_rawseti(L, idx, n); return 0; } catch (...) { return -1; }
    }
    int bridge_next(lua_State* L, int idx) {
        if (reserve(L, 2)) return -1;   // lua_next pushes a key and a value
        try { return lua_next(L, idx); } catch (...) { return -1; }
    }
    int bridge_objlen(lua_State* L, int idx) { return lua_objlen(L, idx); }

    int bridge_ref(lua_State* L, int idx, int* ref) {
        try { *ref = lua_ref(L, idx); return 0; } catch (...) { return -1; }
    }
    int bridge_getref(lua_State* L, int ref) {
        try { lua_rawgeti(L, LUA_REGISTRYINDEX, ref); return 0; } catch (...) { return -1; }
    }
    void bridge_unref(lua_State* L, int ref) { lua_unref(L, ref); }
    void bridge_gc(lua_State* L) {
        try { lua_gc(L, LUA_GCCOLLECT, 0); } catch (...) {}
    }

    int bridge_pushuserdata(lua_State* L, uintptr_t handle) {
        try {
            void* p = lua_newuserdatatagged(L, sizeof(uintptr_t), GO_UDATA_TAG);
            *(uintptr_t*)p = handle;
            return 0;
        } catch (...) { return -1; }
    }

    uintptr_t bridge_touserdata(lua_State* L, int idx) {
        void* p = lua_touserdatatagged(L, idx, GO_UDATA_TAG);
        return p ? *(uintptr_t*)p : 0;   // cgo handles start at 1, so 0 means "none"
    }

    int bridge_setmetatable(lua_State* L, int idx) {
        try { return lua_setmetatable(L, idx); } catch (...) { return 0; }
    }

    lua_State* bridge_newthread(lua_State* L, int* ref) {
        int top = lua_gettop(L);
        try {
            if (reserve(L, 1)) return nullptr;
            lua_State* co = lua_newthread(L);   // pushes the thread
            *ref = lua_ref(L, -1);              // pin it in the registry
            lua_settop(L, top);                 // and take it off the stack
            return co;
        } catch (...) {
            lua_settop(L, top);
            return nullptr;
        }
    }

    int bridge_resume(lua_State* co, lua_State* from, int narg) {
        try {
            return lua_resume(co, from, narg);
        } catch (...) {
            lua_pushstring(co, "internal error");
            return LUA_ERRRUN;
        }
    }

    int bridge_yieldcode(void) { return LUA_YIELD; }
    int bridge_breakcode(void) { return LUA_BREAK; }

    int bridge_costatus(lua_State* L, lua_State* co) {
        switch (lua_costatus(L, co)) {
            case LUA_CORUN: return 0;
            case LUA_COSUS: return 1;
            case LUA_CONOR: return 2;
            case LUA_COFIN: return 3;
            default:        return 4;
        }
    }

    void bridge_sandboxthread(lua_State* L) {
        try { luaL_sandboxthread(L); } catch (...) {}
    }

    int bridge_rawget(lua_State* L, int idx) {
        try { lua_rawget(L, idx); return 0; } catch (...) { return -1; }
    }
    int bridge_rawset(lua_State* L, int idx) {
        try { lua_rawset(L, idx); return 0; } catch (...) { return -1; }
    }

    void bridge_setreadonly(lua_State* L, int idx, int enabled) {
        lua_setreadonly(L, idx, enabled);
    }
}