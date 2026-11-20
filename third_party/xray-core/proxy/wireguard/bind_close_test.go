package wireguard

import "testing"

// monolit: привязку закрывают и устройство, и обработчик — второе закрытие не должно ронять программу
func TestNetBindCloseTwice(t *testing.T) {
	b := &netBind{readQueue: make(chan *netReadInfo), workers: 1}
	if err := b.Close(); err != nil { // ещё не открыта
		t.Fatal(err)
	}
	if _, _, err := b.Open(0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-b.closedCh:
	default:
		t.Fatal("после закрытия канал открыт")
	}
	// открыли заново — снова рабочая, закрывается один раз и повторно
	if _, _, err := b.Open(0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.closedCh:
		t.Fatal("после повторного открытия канал закрыт")
	default:
	}
	b.Close()
	b.Close()
}
