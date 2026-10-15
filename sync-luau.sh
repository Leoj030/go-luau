#!/bin/sh
set -e
SRC=vendor/luau
DST=luau

# remove previously copied sources, keep our own bridge files and luau.go
find $DST -maxdepth 1 \( -name '*.cpp' -o -name '*.h' \) ! -name 'bridge.*' -delete
rm -rf $DST/include && mkdir -p $DST/include

# sources
find $SRC/VM/src $SRC/Common/src $SRC/Bytecode/src $SRC/Ast/src $SRC/Compiler/src \
  \( -name '*.cpp' -o -name '*.h' \) -exec cp {} $DST/ \;

# public headers
for d in Common VM Bytecode Ast Compiler; do
  cp -r $SRC/$d/include/. $DST/include/
done

# license + version stamp
cp $SRC/LICENSE.txt $DST/LICENSE-luau.txt
cp $SRC/lua_LICENSE.txt $DST/LICENSE-lua.txt
git -C $SRC describe --tags --always > $DST/LUAU_VERSION