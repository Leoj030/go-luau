#include <stdlib.h>

#include "bridge.h"
#include "lua.h"
#include "lualib.h"
#include "luacode.h"

extern "C" {

lua_State* bridge_newstate(void) {
    lua_State* L = luaL_newstate();
    if (L) luaL_openlibs(L);
    return L;
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

}