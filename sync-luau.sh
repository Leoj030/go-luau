#!/bin/sh
set -e
SRC=third_party/luau
DST=luau/core

mkdir -p $DST
rm -f $DST/*.cpp $DST/*.h

find luau -maxdepth 1 \( -name '*.cpp' -o -name '*.h' \) ! -name 'bridge.*' -delete
rm -rf luau/include && mkdir -p luau/include

find $SRC/VM/src $SRC/Common/src $SRC/Bytecode/src $SRC/Ast/src $SRC/Compiler/src \
  \( -name '*.cpp' -o -name '*.h' \) -exec cp {} $DST/ \;

for d in Common VM Bytecode Ast Compiler; do
  cp -r $SRC/$d/include/. luau/include/
done

cp $SRC/LICENSE.txt luau/LICENSE-luau.txt
cp $SRC/lua_LICENSE.txt luau/LICENSE-lua.txt
git -C $SRC describe --tags --always > luau/LUAU_VERSION