package buf_test

import (
	"testing"

	"github.com/xtls/xray-core/common"
	. "github.com/xtls/xray-core/common/buf"
)

type closeCount struct{ n int }

func (c *closeCount) Close() error { c.n++; return nil }

type nopReader struct{}

func (nopReader) ReadMultiBuffer() (MultiBuffer, error) { return nil, nil }

// обёртка не должна глотать обрыв: иначе соединения из TUN нельзя оборвать при смене маршрута
func TestTimeoutWrapperInterrupt(t *testing.T) {
	c := &closeCount{}
	inner := &TimeoutWrapperReader{Reader: nopReader{}, Closer: c}
	common.Interrupt(&TimeoutWrapperReader{Reader: inner})
	if c.n != 1 {
		t.Fatalf("closed %d times", c.n)
	}
	common.Interrupt(&TimeoutWrapperReader{Reader: nopReader{}}) // без Closer — без паники
}
