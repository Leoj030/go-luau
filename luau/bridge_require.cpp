#include <stdlib.h>
#include <string.h>

#include "bridge.h"
#include "lua.h"
#include "lualib.h"
#include "luacode.h"
#include "Luau/Require.h"

// Registry table: light userdata (a module thread) -> { module thread, requirer }.
static const char* kWaiters = "_GOLUAU_MODULE_WAITERS";

// Implemented in Go (require_callbacks.go). The strings they return are
// malloc'd, and are freed here.
extern "C" {
    int goReqAllowed(uintptr_t ctx, char* chunkname);
    int goReqReset(uintptr_t ctx, char* chunkname);
    int goReqJump(uintptr_t ctx, char* path);
    int goReqToParent(uintptr_t ctx);
    int goReqToChild(uintptr_t ctx, char* name);
    int goReqPresent(uintptr_t ctx);
    char* goReqName(uintptr_t ctx, int which);   // 0 chunkname, 1 loadname, 2 cache key
    int goReqConfigStatus(uintptr_t ctx);
    char* goReqConfig(uintptr_t ctx, size_t* outlen);
    char* goReqSource(uintptr_t ctx, char* loadname, size_t* outlen, char** errmsg);
    int goReqAsync(uintptr_t ctx);
}

static_assert((int)BRIDGE_NAV_SUCCESS == (int)NAVIGATE_SUCCESS &&
              (int)BRIDGE_NAV_AMBIGUOUS == (int)NAVIGATE_AMBIGUOUS &&
              (int)BRIDGE_NAV_NOT_FOUND == (int)NAVIGATE_NOT_FOUND,
              "navigation result values drifted from Luau's");
static_assert((int)BRIDGE_CFG_ABSENT == (int)CONFIG_ABSENT &&
              (int)BRIDGE_CFG_AMBIGUOUS == (int)CONFIG_AMBIGUOUS &&
              (int)BRIDGE_CFG_JSON == (int)CONFIG_PRESENT_JSON &&
              (int)BRIDGE_CFG_LUAU == (int)CONFIG_PRESENT_LUAU,
              "config status values drifted from Luau's");

static luarequire_NavigateResult nav(int r) {
    if (r == BRIDGE_NAV_SUCCESS) return NAVIGATE_SUCCESS;
    if (r == BRIDGE_NAV_AMBIGUOUS) return NAVIGATE_AMBIGUOUS;
    return NAVIGATE_NOT_FOUND;
}

// Luau's buffer protocol: size_out includes the terminating NUL, and a
// buffer that is too small gets the size it needs.
static luarequire_WriteResult write_out(const char* s, size_t n, char* buffer, size_t bufferSize, size_t* sizeOut) {
    size_t need = n + 1;
    *sizeOut = need;
    if (bufferSize < need) return WRITE_BUFFER_TOO_SMALL;
    memcpy(buffer, s, n);
    buffer[n] = '\0';
    return WRITE_SUCCESS;
}

static luarequire_WriteResult write_name(void* ctx, int which, char* buffer, size_t bufferSize, size_t* sizeOut) {
    char* s = goReqName((uintptr_t)ctx, which);
    if (!s) return WRITE_FAILURE;
    luarequire_WriteResult r = write_out(s, strlen(s), buffer, bufferSize, sizeOut);
    free(s);
    return r;
}

static bool req_is_allowed(lua_State*, void* ctx, const char* chunkname) {
    return goReqAllowed((uintptr_t)ctx, (char*)chunkname) != 0;
}
static luarequire_NavigateResult req_reset(lua_State*, void* ctx, const char* chunkname) {
    return nav(goReqReset((uintptr_t)ctx, (char*)chunkname));
}
static luarequire_NavigateResult req_jump_to_alias(lua_State*, void* ctx, const char* path) {
    return nav(goReqJump((uintptr_t)ctx, (char*)path));
}
static luarequire_NavigateResult req_to_parent(lua_State*, void* ctx) {
    return nav(goReqToParent((uintptr_t)ctx));
}
static luarequire_NavigateResult req_to_child(lua_State*, void* ctx, const char* name) {
    return nav(goReqToChild((uintptr_t)ctx, (char*)name));
}
static bool req_is_module_present(lua_State*, void* ctx) {
    return goReqPresent((uintptr_t)ctx) != 0;
}
static luarequire_WriteResult req_get_chunkname(lua_State*, void* ctx, char* buffer, size_t bufferSize, size_t* sizeOut) {
    return write_name(ctx, 0, buffer, bufferSize, sizeOut);
}
static luarequire_WriteResult req_get_loadname(lua_State*, void* ctx, char* buffer, size_t bufferSize, size_t* sizeOut) {
    return write_name(ctx, 1, buffer, bufferSize, sizeOut);
}
static luarequire_WriteResult req_get_cache_key(lua_State*, void* ctx, char* buffer, size_t bufferSize, size_t* sizeOut) {
    return write_name(ctx, 2, buffer, bufferSize, sizeOut);
}
static luarequire_ConfigStatus req_get_config_status(lua_State*, void* ctx) {
    switch (goReqConfigStatus((uintptr_t)ctx)) {
        case BRIDGE_CFG_AMBIGUOUS: return CONFIG_AMBIGUOUS;
        case BRIDGE_CFG_JSON: return CONFIG_PRESENT_JSON;
        case BRIDGE_CFG_LUAU: return CONFIG_PRESENT_LUAU;
        default: return CONFIG_ABSENT;
    }
}
static luarequire_WriteResult req_get_config(lua_State*, void* ctx, char* buffer, size_t bufferSize, size_t* sizeOut) {
    size_t n = 0;
    char* s = goReqConfig((uintptr_t)ctx, &n);
    if (!s) return WRITE_FAILURE;
    luarequire_WriteResult r = write_out(s, n, buffer, bufferSize, sizeOut);
    free(s);
    return r;
}

// Compiles and runs the module in a thread of its own, and leaves its single
// result on L's stack. Errors are raised with luaL_error and lua_error, which
// throw, so nothing with a destructor may be alive when they are called.
static int req_load(lua_State* L, void* ctx, const char* path, const char* chunkname, const char* loadname) {
    size_t srclen = 0;
    char* errmsg = nullptr;
    char* src = goReqSource((uintptr_t)ctx, (char*)loadname, &srclen, &errmsg);
    if (!src) {
        lua_pushstring(L, errmsg ? errmsg : "could not read module");
        free(errmsg);
        lua_error(L);
    }

    size_t bclen = 0;
    char* bytecode = luau_compile(src, srclen, nullptr, &bclen);
    free(src);
    if (!bytecode) luaL_error(L, "out of memory while compiling module");

    // The module gets a new thread, created from the main thread so it does
    // not inherit L's environment, and with its own sandboxed globals.
    lua_State* GL = lua_mainthread(L);
    lua_State* ML = lua_newthread(GL);
    lua_xmove(GL, L, 1);   // keeps the thread alive on L's stack
    luaL_sandboxthread(ML);

    int status = luau_load(ML, chunkname, bytecode, bclen, 0);
    free(bytecode);
    if (status != 0) {
        lua_xmove(ML, L, 1);   // the syntax error message
        lua_error(L);
    }

    int rstatus = lua_resume(ML, L, 0);
    if (rstatus == 0) {
        if (lua_gettop(ML) != 1) luaL_error(L, "module must return a single value");
    } else if (rstatus == LUA_YIELD) {
        // The module paused, for example to wait on a timer. That is only
        // possible if the host can wake this thread again, and if this thread
        // is allowed to yield itself.
        if (!goReqAsync((uintptr_t)ctx) || !lua_isyieldable(L))
            luaL_error(L, "module can not yield");

        // Remember who waits for the module, so the host can find the
        // requirer when the module finishes. L's stack is now
        //   path, cacheKey, chunkname, loadname, ML
        // and require needs exactly the first four when it yields.
        int mlIdx = lua_gettop(L);
        luaL_findtable(L, LUA_REGISTRYINDEX, kWaiters, 1);   // the table
        lua_pushlightuserdata(L, ML);                         // key
        lua_createtable(L, 2, 0);                             // value
        lua_pushvalue(L, mlIdx);
        lua_rawseti(L, -2, 1);                                // value[1] = ML (keeps it alive)
        lua_pushthread(L);
        lua_rawseti(L, -2, 2);                                // value[2] = the requirer
        lua_rawset(L, mlIdx + 1);                             // table[ML] = value
        lua_settop(L, mlIdx - 1);
        return -1;                                            // require yields the requirer
    } else if (lua_isstring(ML, -1)) {
        luaL_error(L, "error while running module: %s", lua_tostring(ML, -1));
    } else {
        luaL_error(L, "unknown error while running module");
    }

    lua_xmove(ML, L, 1);   // the module's result
    lua_remove(L, -2);     // and drop the thread
    return 1;
}

static void req_config_init(luarequire_Configuration* config) {
    config->is_require_allowed = req_is_allowed;
    config->reset = req_reset;
    config->jump_to_alias = req_jump_to_alias;
    config->to_alias_override = nullptr;
    config->to_alias_fallback = nullptr;
    config->to_parent = req_to_parent;
    config->to_child = req_to_child;
    config->is_module_present = req_is_module_present;
    config->get_chunkname = req_get_chunkname;
    config->get_loadname = req_get_loadname;
    config->get_cache_key = req_get_cache_key;
    config->get_config_status = req_get_config_status;
    config->get_alias = nullptr;            // get_config is set instead, so Luau parses configs itself
    config->get_config = req_get_config;
    config->get_luau_config_timeout = nullptr;
    config->load = req_load;
}

extern "C" {
    int bridge_openrequire(lua_State* L, uintptr_t handle) {
        try {
            if (!lua_checkstack(L, 4)) return -1;
            luaopen_require(L, req_config_init, (void*)handle);
            return 0;
        } catch (...) { return -1; }
    }

    int bridge_registermodule(lua_State* L, int path_idx, int table_idx) {
        int top = lua_gettop(L);
        lua_State* T = nullptr;
        try {
            if (!lua_checkstack(L, 3)) return -1;
            T = lua_newthread(L);
            if (!T || !lua_checkstack(T, 2)) {
                lua_settop(L, top);
                return -1;
            }
            lua_pushvalue(L, path_idx);
            lua_pushvalue(L, table_idx);
            lua_xmove(L, T, 2);
            luarequire_registermodule(T);
            lua_settop(T, 0);
            lua_settop(L, top);
            return 0;
        } catch (...) {
            if (T) lua_settop(T, 0);
            lua_settop(L, top);
            return -1;
        }
    }

    // Both run Luau's own C functions through lua_pcall, so an error becomes
    // a status code and the caller's stack is always put back.
    int bridge_clearrequirecache(lua_State* L) {
        int top = lua_gettop(L);
        try {
            if (!lua_checkstack(L, 2)) return -1;
            lua_pushcfunction(L, luarequire_clearcache, "clearcache");
            int status = lua_pcall(L, 0, 0, 0);
            lua_settop(L, top);
            return status == 0 ? 0 : -1;
        } catch (...) {
            lua_settop(L, top);
            return -1;
        }
    }

    int bridge_clearrequirecacheentry(lua_State* L, const char* key, size_t len) {
        int top = lua_gettop(L);
        try {
            if (!lua_checkstack(L, 3)) return -1;
            lua_pushcfunction(L, luarequire_clearcacheentry, "clearcacheentry");
            lua_pushlstring(L, len ? key : "", len);
            int status = lua_pcall(L, 1, 0, 0);
            lua_settop(L, top);
            return status == 0 ? 0 : -1;
        } catch (...) {
            lua_settop(L, top);
            return -1;
        }
    }

    lua_State* bridge_takewaiter(lua_State* L, lua_State* ml, int* ref) {
        int top = lua_gettop(L);
        try {
            if (!lua_checkstack(L, 5)) return nullptr;
            luaL_findtable(L, LUA_REGISTRYINDEX, kWaiters, 1);   // top+1
            int tbl = top + 1;
            lua_pushlightuserdata(L, ml);
            lua_rawget(L, tbl);                                   // top+2
            lua_State* waiter = nullptr;
            if (lua_istable(L, top + 2)) {
                lua_rawgeti(L, top + 2, 2);                       // top+3
                waiter = lua_tothread(L, top + 3);
                if (waiter) {
                    *ref = lua_ref(L, top + 3);
                    lua_pushlightuserdata(L, ml);
                    lua_pushnil(L);
                    lua_rawset(L, tbl);                           // remove the record
                }
            }
            lua_settop(L, top);
            return waiter;
        } catch (...) {
            lua_settop(L, top);
            return nullptr;
        }
    }

    int bridge_xpush(lua_State* from, lua_State* to) {
        try {
            if (!lua_checkstack(to, 1)) return -1;
            lua_xpush(from, to, -1);
            return 0;
        } catch (...) { return -1; }
    }
}