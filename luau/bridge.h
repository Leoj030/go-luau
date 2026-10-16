#include <stddef.h>
#include <stdint.h>

#ifndef BRIDGE_H
#define BRIDGE_H

#ifdef __cplusplus

extern "C" {
    #endif

    typedef struct lua_State lua_State;

    lua_State* bridge_newstate(uintptr_t handle);   // handle identifies the Go State
    uintptr_t bridge_getuserdata(lua_State* L);
    int bridge_pushgofunction(lua_State* L, int id, const char* name);
    void bridge_close(lua_State* L);

    // Compile + load + run. Returns 0 on success. On failure, the error message
    // is left on top of the stack (read it with bridge_tostring).
    int bridge_dostring(lua_State* L, const char* src, size_t len, const char* chunkname);

    const char* bridge_tostring(lua_State* L, int idx, size_t* len);

    int bridge_gettop(lua_State* L);
    void bridge_settop(lua_State* L, int idx);
    int bridge_type(lua_State* L, int idx);
    const char* bridge_typename(lua_State* L, int idx);

    // push functions return 0 on success, -1 on failure (e.g. out of memory)
    int bridge_pushnil(lua_State* L);
    int bridge_pushboolean(lua_State* L, int b);
    int bridge_pushnumber(lua_State* L, double n);
    int bridge_pushlstring(lua_State* L, const char* s, size_t len);

    int bridge_toboolean(lua_State* L, int idx);
    double bridge_tonumber(lua_State* L, int idx, int* ok);

    // getglobal pushes the value, setglobal pops it. Both can run metamethods.
    int bridge_getglobal(lua_State* L, const char* name);
    int bridge_setglobal(lua_State* L, const char* name);

    #ifdef __cplusplus
}

#endif
#endif