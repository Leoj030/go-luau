#ifndef BRIDGE_H
#define BRIDGE_H
#include <stddef.h>
#ifdef __cplusplus
extern "C" {
#endif

typedef struct lua_State lua_State;

lua_State* bridge_newstate(void);
void bridge_close(lua_State* L);

// Compile + load + run. Returns 0 on success. On failure, the error message
// is left on top of the stack (read it with bridge_tostring).
int bridge_dostring(lua_State* L, const char* src, size_t len, const char* chunkname);

const char* bridge_tostring(lua_State* L, int idx, size_t* len);
void bridge_settop(lua_State* L, int idx);

#ifdef __cplusplus
}
#endif
#endif