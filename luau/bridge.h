#include <stddef.h>
#include <stdint.h>

#ifndef BRIDGE_H
#define BRIDGE_H

#ifdef __cplusplus

extern "C" {
    #endif

    typedef struct lua_State lua_State;

    // Library flags for bridge_newstate. 0 means "luaL_openlibs" (the default set).
    enum {
        BRIDGE_LIB_BASE      = 1 << 0,
        BRIDGE_LIB_COROUTINE = 1 << 1,
        BRIDGE_LIB_TABLE     = 1 << 2,
        BRIDGE_LIB_OS        = 1 << 3,
        BRIDGE_LIB_STRING    = 1 << 4,
        BRIDGE_LIB_BIT32     = 1 << 5,
        BRIDGE_LIB_BUFFER    = 1 << 6,
        BRIDGE_LIB_UTF8      = 1 << 7,
        BRIDGE_LIB_CLASS     = 1 << 8,
        BRIDGE_LIB_MATH      = 1 << 9,
        BRIDGE_LIB_DEBUG     = 1 << 10,
        BRIDGE_LIB_VECTOR    = 1 << 11,
        BRIDGE_LIB_INTEGER   = 1 << 12,
        BRIDGE_LIB_NONE      = 1 << 30
    };

    // memlimit is in bytes; 0 means unlimited.
    lua_State* bridge_newstate(uintptr_t handle, unsigned int libs, size_t memlimit);

    size_t bridge_memused(lua_State* L);
    int bridge_errmem(void);   // the status code Luau uses for out-of-memory

    // Interrupt flag. bridge_interrupt is safe to call from any thread.
    void bridge_interrupt(lua_State* L);
    void bridge_clearinterrupt(lua_State* L);
    int bridge_isinterrupted(lua_State* L);

    void bridge_sandbox(lua_State* L);
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

    lua_State* bridge_newthread(lua_State* L, int* ref);   // thread is referenced, not left on the stack
    int bridge_resume(lua_State* co, lua_State* from, int narg);   // raw Luau status
    int bridge_yieldcode(void);    // the status lua_resume returns for "yielded"
    int bridge_breakcode(void);    // ... and for "hit a breakpoint"
    int bridge_costatus(lua_State* L, lua_State* co);   // 0 running, 1 suspended, 2 normal, 3 finished, 4 failed
    void bridge_sandboxthread(lua_State* L);
    
    // Parses src and reports the first syntax error with its position (1-based).
    // Returns 1 and a malloc'd message (free it) if there is an error, else 0.
    int bridge_parseerror(const char* src, size_t len, int* line, int* col, char** msg);

    // Traceback of the last error. Valid until the next error; copy it at once.
    const char* bridge_traceback(lua_State* L, size_t* len);
    void bridge_cleartraceback(lua_State* L);

    // Captures the traceback of a thread that has just failed in lua_resume.
    void bridge_tracethread(lua_State* co);

    int bridge_rawget(lua_State* L, int idx);   // pops a key, pushes t[key]
    int bridge_rawset(lua_State* L, int idx);   // pops a key and a value, sets t[key] = value

    #ifdef __cplusplus
}

#endif
#endif