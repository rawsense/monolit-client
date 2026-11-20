package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Вызовы интерфейса по имени: один вход для оболочек, у которых окно не умеет привязывать функции Go
// (Android). Те же привязки, что у окна Windows, — список в bindings

var (
	rpcOnce sync.Once
	rpcMap  map[string]reflect.Value
)

// Call выполняет привязку name. args — JSON-массив аргументов, ответ — JSON результата (null, если его нет).
// Авария внутри привязки возвращается ошибкой
func (a *App) Call(name string, args []byte) ([]byte, error) {
	rpcOnce.Do(func() {
		rpcMap = map[string]reflect.Value{}
		for n, fn := range a.bindings() {
			rpcMap[n] = reflect.ValueOf(safeBinding(n, fn))
		}
	})
	v, ok := rpcMap[name]
	if !ok {
		return nil, errors.New("нет такого вызова: " + name)
	}
	var raw []json.RawMessage
	if len(args) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, err
		}
	}
	t := v.Type()
	if len(raw) != t.NumIn() {
		return nil, fmt.Errorf("%s: аргументов %d, нужно %d", name, len(raw), t.NumIn())
	}
	in := make([]reflect.Value, len(raw))
	for i := range raw {
		p := reflect.New(t.In(i))
		if err := json.Unmarshal(raw[i], p.Interface()); err != nil {
			return nil, err
		}
		in[i] = p.Elem()
	}
	out := v.Call(in) // у обёртки safeBinding последним всегда идёт error
	if err, _ := out[len(out)-1].Interface().(error); err != nil {
		return nil, err
	}
	if len(out) == 1 {
		return []byte("null"), nil
	}
	return json.Marshal(out[0].Interface())
}
