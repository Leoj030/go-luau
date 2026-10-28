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
    std::string scratch;         // output buffer of bridge_encode, reused between calls
};

static Host* host_of(lua_State* L) { return (Host*)lua_callbacks(L)->userdata; }

// Makes room for n more stack slots before something is pushed. Luau does
// not check this in release builds, so every function below that pushes
// calls it first. Returns 0 on success and -1 if the stack can't grow.
static int reserve(lua_State* L, int n) {
    try { return lua_checkstack(L, n) ? 0 : -1; }
    catch (...) { return -1; }
}

// ---- wire format -----------------------------------------------------------
// Shared with luau/wire.go. Numbers are little-endian.
//   0 nil      1 false     2 true
//   3 number   8 bytes, the bits of a double
//   4 string   u32 length, then the bytes
//   5 array    u32 n, then n values (the keys 1..n)                  [written by Go]
//   6 map      u32 n, then n (key, value) pairs
//   7 bad      u32 length, then the Lua type name: a value or key that can't be converted
//   8 deep     a table nested too deeply
// A "bad" or "deep" marker is not an error by itself: the Go side only complains
// if it actually needs that value, so a table can hold a function that nobody asks for.
enum { W_NIL = 0, W_FALSE, W_TRUE, W_NUM, W_STR, W_ARR, W_MAP, W_BAD, W_DEEP };

static void put_u32(std::string& out, uint32_t v) {
    char b[4] = { (char)(v & 0xff), (char)((v >> 8) & 0xff), (char)((v >> 16) & 0xff), (char)((v >> 24) & 0xff) };
    out.append(b, 4);
}

static void put_u64(std::string& out, uint64_t v) {
    char b[8];
    for (int i = 0; i < 8; i++) b[i] = (char)((v >> (8 * i)) & 0xff);
    out.append(b, 8);
}

static void put_bytes(std::string& out, const char* s, size_t n) {
    put_u32(out, (uint32_t)n);
    out.append(s, n);
}

static void enc(lua_State* L, int idx, std::string& out, int depth, int maxdepth, int* err);

// Keys that aren't a string, number or boolean become a "bad" marker.
static void enc_key(lua_State* L, int idx, std::string& out) {
    int t = lua_type(L, idx);
    if (t == LUA_TSTRING || t == LUA_TNUMBER || t == LUA_TBOOLEAN) {
        int err = 0;
        enc(L, idx, out, 0, 0, &err);   // a scalar never recurses
        return;
    }
    const char* name = lua_typename(L, t);
    out.push_back((char)W_BAD);
    put_bytes(out, name, strlen(name));
}

// Encodes the value at the absolute index idx.
static void enc(lua_State* L, int idx, std::string& out, int depth, int maxdepth, int* err) {
    switch (lua_type(L, idx)) {
    case LUA_TNIL:
        out.push_back((char)W_NIL);
        return;
    case LUA_TBOOLEAN:
        out.push_back((char)(lua_toboolean(L, idx) ? W_TRUE : W_FALSE));
        return;
    case LUA_TNUMBER: {
        double d = lua_tonumber(L, idx);
        uint64_t bits;
        memcpy(&bits, &d, sizeof bits);
        out.push_back((char)W_NUM);
        put_u64(out, bits);
        return;
    }
    case LUA_TSTRING: {
        size_t n = 0;
        const char* s = lua_tolstring(L, idx, &n);
        out.push_back((char)W_STR);
        put_bytes(out, s, n);
        return;
    }
    case LUA_TTABLE:
        break;
    default: {
        const char* name = lua_typename(L, lua_type(L, idx));
        out.push_back((char)W_BAD);
        put_bytes(out, name, strlen(name));
        return;
    }
    }

    if (depth >= maxdepth) {
        out.push_back((char)W_DEEP);
        return;
    }
    if (!lua_checkstack(L, 4)) {
        *err = BRIDGE_ENC_STACK;
        return;
    }

    out.push_back((char)W_MAP);
    size_t countAt = out.size();
    put_u32(out, 0);
    uint32_t count = 0;

    lua_pushnil(L);
    while (lua_next(L, idx) != 0) {
        int top = lua_gettop(L);          // the value is at top, its key at top - 1
        enc_key(L, top - 1, out);
        enc(L, top, out, depth + 1, maxdepth, err);
        if (*err) return;                 // the caller restores the stack
        count++;
        lua_settop(L, top - 1);           // pop the value, keep the key for the next step
    }
    for (int i = 0; i < 4; i++) out[countAt + i] = (char)((count >> (8 * i)) & 0xff);
}

static bool rd_u32(const unsigned char*& p, const unsigned char* end, uint32_t* v) {
    if (end - p < 4) return false;
    *v = (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
    p += 4;
    return true;
}

// Reads one value from the wire format and pushes it. Returns false if the
// input is malformed.
static bool build(lua_State* L, const unsigned char*& p, const unsigned char* end, int depth) {
    if (p >= end || depth > 200) return false;
    unsigned char tag = *p++;
    switch (tag) {
    case W_NIL:
        lua_pushnil(L);
        return true;
    case W_FALSE:
        lua_pushboolean(L, 0);
        return true;
    case W_TRUE:
        lua_pushboolean(L, 1);
        return true;
    case W_NUM: {
        if (end - p < 8) return false;
        uint64_t bits = 0;
        for (int i = 0; i < 8; i++) bits |= (uint64_t)p[i] << (8 * i);
        p += 8;
        double d;
        memcpy(&d, &bits, sizeof d);
        lua_pushnumber(L, d);
        return true;
    }
    case W_STR: {
        uint32_t n;
        if (!rd_u32(p, end, &n) || (size_t)(end - p) < n) return false;
        lua_pushlstring(L, n ? (const char*)p : "", n);
        p += n;
        return true;
    }
    case W_ARR: {
        uint32_t n;
        if (!rd_u32(p, end, &n) || n > (size_t)(end - p) || !lua_checkstack(L, 4)) return false;
        lua_createtable(L, (int)n, 0);
        for (uint32_t i = 1; i <= n; i++) {
            if (!build(L, p, end, depth + 1)) return false;
            lua_rawseti(L, -2, (int)i);
        }
        return true;
    }
    case W_MAP: {
        uint32_t n;
        if (!rd_u32(p, end, &n) || n > (size_t)(end - p) || !lua_checkstack(L, 4)) return false;
        lua_createtable(L, 0, (int)n);
        for (uint32_t i = 0; i < n; i++) {
            if (!build(L, p, end, depth + 1)) return false;   // the key
            if (!build(L, p, end, depth + 1)) return false;   // the value
            lua_rawset(L, -3);
        }
        return true;
    }
    }
    return false;
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

    int bridge_pinthread(lua_State* L, int* ref) {
        int top = lua_gettop(L);
        try {
            if (reserve(L, 1)) return -1;
            if (lua_pushthread(L)) {   // returns 1 for the main thread
                lua_settop(L, top);
                return 1;
            }
            *ref = lua_ref(L, -1);
            lua_settop(L, top);
            return 0;
        } catch (...) {
            lua_settop(L, top);
            return -1;
        }
    }

    int bridge_resetthread(lua_State* main, lua_State* co) {
        int st = lua_costatus(main, co);
        if (st == LUA_CORUN || st == LUA_CONOR) return -1;   // running, or waiting for a thread it resumed
        lua_resetthread(co);
        return 0;
    }

    int bridge_resume(lua_State* co, lua_State* from, int narg) {
        try {
            return lua_resume(co, from, narg);
        } catch (...) {
            lua_pushstring(co, "internal error");
            return LUA_ERRRUN;
        }
    }

    int bridge_resumeerror(lua_State* co, lua_State* from) {
        try {
            return lua_resumeerror(co, from);
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

    int bridge_typeof(lua_State* L, int idx) {
        int top = lua_gettop(L);
        if (!((idx >= 1 && idx <= top) || (idx <= -1 && idx >= -top))) return BRIDGE_T_NONE;
        switch (lua_type(L, idx)) {
            case LUA_TNIL: return BRIDGE_T_NIL;
            case LUA_TBOOLEAN: return BRIDGE_T_BOOLEAN;
            case LUA_TNUMBER: return BRIDGE_T_NUMBER;
            case LUA_TSTRING: return BRIDGE_T_STRING;
            case LUA_TTABLE: return BRIDGE_T_TABLE;
            case LUA_TFUNCTION: return BRIDGE_T_FUNCTION;
            case LUA_TUSERDATA: return BRIDGE_T_USERDATA;
            case LUA_TTHREAD: return BRIDGE_T_THREAD;
            default: return BRIDGE_T_OTHER;
        }
    }

    int bridge_pop(lua_State* L, int n) {
        int top = lua_gettop(L);
        if (n < 0 || n > top) return -1;
        lua_settop(L, top - n);
        return 0;
    }

    // Pushes t[key] for the table at idx.
    int bridge_getfield(lua_State* L, int idx, const char* key, size_t klen) {
        int top = lua_gettop(L);
        if (idx < 0) idx = top + idx + 1;
        if (idx < 1 || idx > top) return 1;
        if (lua_type(L, idx) != LUA_TTABLE) return 2;
        if (reserve(L, 1)) return 4;
        try {
            lua_pushlstring(L, klen ? key : "", klen);
            lua_rawget(L, idx);
            return 0;
        } catch (...) {
            lua_settop(L, top);
            return 3;
        }
    }

    // Does t[key] = value for the table at idx and the value on top, and pops the value.
    int bridge_setfield(lua_State* L, int idx, const char* key, size_t klen) {
        int top = lua_gettop(L);
        if (idx < 0) idx = top + idx + 1;
        if (top < 2 || idx < 1 || idx >= top) return 1;
        if (lua_type(L, idx) != LUA_TTABLE) return 2;
        if (reserve(L, 1)) return 4;
        try {
            lua_pushlstring(L, klen ? key : "", klen);
            lua_insert(L, -2);       // the key goes below the value
            lua_rawset(L, idx);      // pops both
            return 0;
        } catch (...) {
            lua_settop(L, top - 1);
            return 3;
        }
    }

    // Encodes the value at idx into the host's reusable buffer. The data stays
    // valid until the next call that uses the buffer, so the caller copies or parses it at once.
    int bridge_encode(lua_State* L, int idx, int maxdepth, const char** data, size_t* len) {
        int top = lua_gettop(L);
        if (idx < 0) idx = top + idx + 1;
        if (idx < 1 || idx > top) return BRIDGE_ENC_BADINDEX;
        Host* h = host_of(L);
        int err = BRIDGE_ENC_OK;
        try {
            if (h->scratch.capacity() > (1u << 20)) std::string().swap(h->scratch);   // don't keep a big buffer
            h->scratch.clear();
            enc(L, idx, h->scratch, 0, maxdepth, &err);
        } catch (...) {
            lua_settop(L, top);
            return BRIDGE_ENC_MEMORY;
        }
        lua_settop(L, top);
        if (err) return err;
        *data = h->scratch.data();
        *len = h->scratch.size();
        return BRIDGE_ENC_OK;
    }

    // Returns 0, -1 (stack full or out of memory) or -2 (malformed input).
    int bridge_pushencoded(lua_State* L, const char* data, size_t len) {
        int top = lua_gettop(L);
        try {
            if (!lua_checkstack(L, 4)) return -1;
            const unsigned char* p = (const unsigned char*)data;
            const unsigned char* end = p + len;
            if (!build(L, p, end, 0) || p != end) {
                lua_settop(L, top);
                return -2;
            }
            return 0;
        } catch (...) {
            lua_settop(L, top);
            return -1;
        }
    }
}