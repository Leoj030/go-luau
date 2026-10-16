#include <stdlib.h>

#include "bridge.h"
#include "lua.h"
#include "lualib.h"
#include "luacode.h"

extern "C" int goCallback(lua_State* L, int id);  // implemented in Go

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

    int bridge_dostring(lua_State* L, const char* src, size_t len, const char* chunkname) {
        try {
            size_t bcsize = 0;
            char* bytecode = luau_compile(src, len, nullptr, &bcsize);
            int status = luau_load(L, chunkname, bytecode, bcsize, 0);
            free(bytecode);  // luau_compile returns a malloc'd buffer
            if (status != 0) return status;  // syntax errors land here, message on the stack
            return lua_pcall(L, 0, 0, 0);    // protected: runtime errors become a status code
        } catch (...) {
            // Never let a C++ exception unwind into Go
            lua_pushstring(L, "internal error");
            return LUA_ERRRUN;
        }
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
}