#include <stddef.h>
#include <stdint.h>

#ifndef BRIDGE_H
#define BRIDGE_H

#ifdef __cplusplus

extern "C" {
    #endif

    typedef struct lua_State lua_State;

    lua_State* bridge_newstate(uintptr_t handle);
    uintptr_t bridge_getuserdata(lua_State* L);
    int bridge_pushgofunction(lua_State* L, int id, const char* name);
    void bridge_close(lua_State* L);

    // Returns a malloc'd buffer (free it) or NULL on allocation failure.
    // Syntax errors are encoded in the bytecode and reported by bridge_load.
    char* bridge_compile(const char* src, size_t len, size_t* outlen);

    // Returns 0 on success (function pushed). On failure the message is on the stack.
    int bridge_load(lua_State* L, const char* chunkname, const char* data, size_t size);

    // Protected call. Returns 0 on success. On failure the error is on the stack.
    int bridge_pcall(lua_State* L, int nargs, int nresults);

    int bridge_checkstack(lua_State* L, int n);

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

    int bridge_createtable(lua_State* L, int narr, int nrec);
    int bridge_rawgetfield(lua_State* L, int idx, const char* k);  // pushes t[k]
    int bridge_rawsetfield(lua_State* L, int idx, const char* k);  // t[k] = pop()
    int bridge_rawgeti(lua_State* L, int idx, int n);              // pushes t[n]
    int bridge_rawseti(lua_State* L, int idx, int n);              // t[n] = pop()
    int bridge_next(lua_State* L, int idx);   // 1 = pushed key,value; 0 = end; -1 = error
    int bridge_objlen(lua_State* L, int idx);

    int bridge_ref(lua_State* L, int idx, int* ref);  // pins the value, doesn't pop it
    int bridge_getref(lua_State* L, int ref);         // pushes the referenced value
    void bridge_unref(lua_State* L, int ref);
    void bridge_gc(lua_State* L); // Runs a full garbage collection cycle.

    int bridge_pushuserdata(lua_State* L, uintptr_t handle);
    uintptr_t bridge_touserdata(lua_State* L, int idx);   // 0 if not one of ours
    int bridge_setmetatable(lua_State* L, int idx);       // pops a table from the stack

    #ifdef __cplusplus
}

#endif
#endif