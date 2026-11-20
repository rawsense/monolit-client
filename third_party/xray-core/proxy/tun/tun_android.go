//go:build android

package tun

import (
	"context"
	"strconv"
	"sync"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/platform"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

type AndroidTun struct {
	tunFd   int
	options TunOptions
	once    sync.Once
}

// DefaultTun implements Tun
var _ Tun = (*AndroidTun)(nil)

// DefaultTun implements GVisorTun
var _ GVisorTun = (*AndroidTun)(nil)

// NewTun builds new tun interface handler
// monolit: дескриптор VPN-сервиса берётся из окружения и с этого момента принадлежит ядру. Без него — ошибка:
// раньше подставлялся 0 (стандартный ввод процесса), и ядро настраивало и читало его как TUN
func NewTun(options TunOptions) (Tun, error) {
	fd, err := strconv.Atoi(platform.NewEnvFlag(platform.TunFdKey).GetValue(func() string { return "" }))
	if err != nil || fd <= 0 {
		return nil, errors.New("no Android TUN fd in ", platform.TunFdKey)
	}
	errors.LogInfo(context.Background(), "read Android Tun Fd ", fd)

	err = unix.SetNonblock(fd, true)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	return &AndroidTun{
		tunFd:   fd,
		options: options,
	}, nil
}

func (t *AndroidTun) Start() error {
	return nil
}

// Close закрывает дескриптор (monolit): иначе каждый перезапуск ядра оставлял открытый TUN прошлого
// подключения, и Android не снимал значок VPN, пока жив процесс
func (t *AndroidTun) Close() error {
	t.once.Do(func() { _ = unix.Close(t.tunFd) })
	return nil
}

func (t *AndroidTun) newEndpoint() (stack.LinkEndpoint, error) {
	return fdbased.New(&fdbased.Options{
		FDs:               []int{t.tunFd},
		MTU:               t.options.MTU,
		RXChecksumOffload: true,
	})
}
