package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// вызов по имени: результат в JSON, ошибка привязки и неверные аргументы — ошибкой
func TestCall(t *testing.T) {
	a := newApp(true)
	res, err := a.Call("mpState", nil)
	if err != nil {
		t.Fatal(err)
	}
	var v View
	if err := json.Unmarshal(res, &v); err != nil || v.Version != version {
		t.Fatalf("состояние: %s, %v", res, err)
	}
	if _, err := a.Call("mpMode", []byte(`["нет"]`)); err == nil || !strings.Contains(err.Error(), "режима") {
		t.Fatalf("ошибка привязки не дошла: %v", err)
	}
	if res, err := a.Call("mpMode", []byte(`["all"]`)); err != nil || string(res) != "null" {
		t.Fatalf("вызов без результата: %s, %v", res, err)
	}
	if _, err := a.Call("mpMode", []byte(`[]`)); err == nil {
		t.Fatal("прошёл вызов без аргумента")
	}
	if _, err := a.Call("mpNope", nil); err == nil {
		t.Fatal("прошёл вызов, которого нет")
	}
}
