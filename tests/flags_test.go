package tests

import (
	"testing"

	"github.com/Leoj030/go-luau/luau"
)

func TestClassWhenEnabled(t *testing.T) {
	flags := []string{ "DebugLuauUserDefinedClasses", "DebugLuauUserDefinedClassesRuntime" }

	for _, flag := range flags {
		if _, exists := luau.FastFlag(flag); !exists {
			t.Skip("this Luau version has no " + flag)
		}
	}
	if err := luau.SetFastFlag(flags[0], true); err != nil {
		t.Fatal(err)
	}
	if err := luau.SetFastFlag(flags[1], true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, flag := range flags {
			luau.SetFastFlag(flag, false)
		}
	})

	L, err := luau.NewState()
	if err != nil {
		panic(err)
	}
	defer L.Close()

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
