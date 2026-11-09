package tests

import (
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

func setFlags(t *testing.T, flags []string, b bool) {
	t.Helper()
	for _, flag := range flags {
		if _, exists := luau.FastFlag(flag); !exists {
			t.Skip("this Luau version has no " + flag)
		}
		if err := luau.SetFastFlag(flag, b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClassSyntax(t *testing.T) {
	flags := []string{"DebugLuauUserDefinedClasses", "DebugLuauUserDefinedClassesRuntime"}
	setFlags(t, flags, true)
	t.Cleanup(func() { setFlags(t, flags, false) })

	L := newState(t)
	if err := L.DoString(`
		class Point
			public x: number
			public y

			function length(self)
				return math.sqrt(self.x * self.x + self.y * self.y)
			end

			function __add(self, other: Point)
				return Point { x = self.x + other.x, y = self.y + other.y }
			end

			function __tostring(self)
				return "Point { x = " .. tostring(self.x) .. ", y = " .. tostring(self.y) .. " }"
			end
		end

		local p = Point { x = 3, y = 4 }
		print("Check out my cool point: " .. tostring(p) .. "  length = " .. tostring(p:length()))
	`); err != nil {
		t.Fatal(err)
	}
}

func TestLocalConditionSyntax(t *testing.T) {
	flags := []string{"LuauExperimentalIfLocalSyntax", "LuauExperimentalWhileLocalSyntax"}
	setFlags(t, flags, true)
	t.Cleanup(func() { setFlags(t, flags, false) })

	L := newState(t)
	err := L.DoString(`
		local queue = {3, 2, 1}
		local total = 0
		while local item = table.remove(queue) do
			total += item
		end
		whileresult = total

		if local n = tonumber("42") then
			ifresult = n * 2
		end

		if local a = tonumber("x") then
			elseresult = -1
		elseif local b = tonumber("7") then
			elseresult = b
		end

		leaked = n`)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{
		"whileresult": 6.0,
		"ifresult":    84.0,
		"elseresult":  7.0,
		"leaked":      nil,
	} {
		if got, _ := L.GetGlobal(name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
}
