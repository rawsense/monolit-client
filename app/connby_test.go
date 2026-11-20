package main

import (
	"encoding/json"
	"testing"
)

// выбор подключения запоминается для каждой подписки и возвращается при возврате к ней
func TestConnRememberedPerSub(t *testing.T) {
	s := Settings{Active: "s1"}
	s.pickConn("Для игр")
	s.useSub("s2")
	if s.Active != "s2" || s.Conn != "" {
		t.Fatalf("новая подписка: %q %q", s.Active, s.Conn)
	}
	s.pickConn("Финляндия")
	s.useSub("s1")
	if s.Conn != "Для игр" {
		t.Fatalf("вернулись в s1, выбрано %q", s.Conn)
	}
	s.useSub("s2")
	if s.Conn != "Финляндия" {
		t.Fatalf("вернулись в s2, выбрано %q", s.Conn)
	}
	// переживает сохранение настроек
	raw, _ := json.Marshal(s)
	var back Settings
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	back.useSub("s1")
	if back.Conn != "Для игр" {
		t.Fatalf("после сохранения: %q", back.Conn)
	}
}

// настройки старой версии: выбор есть только в Conn — при первом переключении он не теряется
func TestConnFromOldSettings(t *testing.T) {
	s := Settings{Active: "s1", Conn: "Обход"}
	s.useSub("s2")
	s.useSub("s1")
	if s.Conn != "Обход" {
		t.Fatalf("старый выбор потерян: %q", s.Conn)
	}
}

func TestConnForgottenWithSub(t *testing.T) {
	s := Settings{Active: "s1"}
	s.pickConn("А")
	s.useSub("s2")
	s.pickConn("Б")
	s.dropSub("s1") // не текущая: текущая и её выбор на месте
	if s.Active != "s2" || s.Conn != "Б" || s.ConnBy["s1"] != "" {
		t.Fatalf("после удаления чужой подписки: %+v", s)
	}
	s.dropSub("s2") // текущая
	if s.Active != "" || s.Conn != "" || len(s.ConnBy) != 0 {
		t.Fatalf("после удаления текущей: %+v", s)
	}
	s.useSub("s1") // id занят новой подпиской: старого выбора нет
	if s.Conn != "" {
		t.Fatalf("новая подписка унаследовала выбор: %q", s.Conn)
	}
}

// копия настроек не делит карту с оригиналом
func TestConnByCloned(t *testing.T) {
	s := Settings{Active: "s1"}
	s.pickConn("А")
	c := s.clone()
	c.pickConn("Б")
	if s.ConnBy["s1"] != "А" {
		t.Fatalf("копия изменила оригинал: %v", s.ConnBy)
	}
}

// запомненного подключения в подписке больше нет — берётся первое
func TestConnGoneFallsBackToFirst(t *testing.T) {
	sub := Sub{Conns: []Conn{{Tag: "px-0", Name: "Первый"}, {Tag: "px-1", Name: "Второй"}}}
	if got := pickConns(Settings{Conn: "Удалённый"}, sub); len(got) != 1 || got[0].Name != "Первый" {
		t.Fatalf("%+v", got)
	}
	if got := pickConns(Settings{Conn: "Второй"}, sub); len(got) != 1 || got[0].Name != "Второй" {
		t.Fatalf("%+v", got)
	}
}
