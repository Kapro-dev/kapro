package cel

import (
	"reflect"
	"testing"
)

func TestCachedCELProgram_ReusesCompiledProgram(t *testing.T) {
	env, err := sharedCELEnv()
	if err != nil {
		t.Fatalf("sharedCELEnv returned error: %v", err)
	}
	first, err := cachedCELProgram(env, `args.env == "prod"`)
	if err != nil {
		t.Fatalf("cachedCELProgram returned error: %v", err)
	}
	second, err := cachedCELProgram(env, `args.env == "prod"`)
	if err != nil {
		t.Fatalf("cachedCELProgram returned error: %v", err)
	}
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("expected cachedCELProgram to reuse compiled program")
	}
}
