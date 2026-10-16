#include <stdlib.h>

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
static int trampoline(lua_State* L) {
    int id = (int)lua_tonumber(L, lua_upvalueindex(1));
    int n = goCallback(L, id);   // returns result count, or -1 on error
    if (n < 0) {
        // Go has already returned, so throwing here only unwinds C++ frames.
        if (lua_gettop(L) == 0) lua_pushstring(L, "go function failed");
        lua_error(L);            // error message is the value on top of the stack
    }
    return n;
}

extern "C" {
    lua_State* bridge_newstate(uintptr_t handle) {
        lua_State* L = luaL_newstate();
        if (L) {
            luaL_openlibs(L);
            // Stash the Go State handle so callbacks can find their way back.
            // Callbacks are shared by all coroutines of this state.
            lua_callbacks(L)->userdata = (void*)handle;
            lua_setuserdatadtor(L, GO_UDATA_TAG, go_udata_dtor);
        }
        return L;
    }

    uintptr_t bridge_getuserdata(lua_State* L) {
        return (uintptr_t)lua_callbacks(L)->userdata;
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

    void bridge_close(lua_State* L) { lua_close(L); }

    char* bridge_compile(const char* src, size_t len, size_t* outlen) {
        try {
            return luau_compile(src, len, nullptr, outlen);
        } catch (...) {
            *outlen = 0;
            return nullptr;
        }
    }

    int bridge_load(lua_State* L, const char* chunkname, const char* data, size_t size) {
        try {
            return luau_load(L, chunkname, data, size, 0);
        } catch (...) {
            lua_pushstring(L, "internal error");
            return LUA_ERRMEM;
        }
    }

    int bridge_pcall(lua_State* L, int nargs, int nresults) {
        try {
            return lua_pcall(L, nargs, nresults, 0);
        } catch (...) {
            lua_pushstring(L, "internal error");
            return LUA_ERRRUN;
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

    int bridge_pushnil(lua_State* L) { lua_pushnil(L); return 0; }
    int bridge_pushboolean(lua_State* L, int b) { lua_pushboolean(L, b); return 0; }
    int bridge_pushnumber(lua_State* L, double n) { lua_pushnumber(L, n); return 0; }
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
}