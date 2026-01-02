package mobile

import (
    "fmt"
    "io"
    "log"
    "os"
    "time"

    // core engine + SOCKS handler
    "github.com/eycorsican/go-tun2socks/core"
    "github.com/eycorsican/go-tun2socks/proxy/socks"
)

// SocksConfig is the configuration we’ll pass from Kotlin.
type SocksConfig struct {
    Host string
    Port int // SOCKS5 port
}

// TunnelHandle is just a dummy handle so gomobile gives us
// a Java/Kotlin object to pass back and forth.
type TunnelHandle struct {
    started bool
    // keep references so we can shutdown cleanly
    stack core.LWIPStack
    dev   io.ReadWriteCloser
}

// StartSocksTunnel wires a TUN file descriptor (from Android VpnService)
// to a remote SOCKS5 server (e.g., mitmproxy in socks mode).
//
// fd   – the integer fd from VpnService.Builder.establish().detachFd()
// host – SOCKS5 server address (PC with mitmproxy)
// port – SOCKS5 port
func StartSocksTunnel(fd int, host string, port int) (*TunnelHandle, error) {
    if host == "" || port <= 0 || port > 65535 {
        return nil, fmt.Errorf("invalid SOCKS endpoint %s:%d", host, port)
    }
    // Wrap the fd into an *os.File which implements ReadWriteCloser.
    // On Android the TUN fd from VpnService can be used this way.
    f := os.NewFile(uintptr(fd), "tun")
    if f == nil {
        return nil, fmt.Errorf("failed to create file from fd %d", fd)
    }

    // 1) Create lwIP stack and keep reference so we can Close() it later.
    stack := core.NewLWIPStack()
    lwipWriter := stack.(io.Writer)

    // 2) Register output callback so lwip can write packets to TUN device.
    core.RegisterOutputFn(func(data []byte) (int, error) {
        return f.Write(data)
    })

    // 3) Build SOCKS handlers (TCP + UDP) and register them.
    const dialTimeout = 5 * time.Second
    const udpTimeout = 60 * time.Second

    // The repo provides NewTCPHandler and NewUDPHandler. Use those.
    tcpHandler := socks.NewTCPHandler(host, uint16(port))
    udpHandler := socks.NewUDPHandler(host, uint16(port), udpTimeout)

    core.RegisterTCPConnHandler(tcpHandler)
    core.RegisterUDPConnHandler(udpHandler)

    // 4) Start copying packets from the TUN device into lwIP stack.
    const MTU = 1500
    go func() {
        if _, err := io.CopyBuffer(lwipWriter, f, make([]byte, MTU)); err != nil {
            log.Printf("copy tun->lwip failed: %v", err)
        }
    }()

    return &TunnelHandle{started: true, stack: stack, dev: f}, nil
}

// StopTunnel stops the event loop and cleans up.
// On the Java/Kotlin side you just call this and then
// close your fd.
func StopTunnel(h *TunnelHandle) {
    if h == nil || !h.started {
        return
    }

    // unregister handlers
    core.RegisterTCPConnHandler(nil)
    core.RegisterUDPConnHandler(nil)

    // close lwip stack
    if h.stack != nil {
        h.stack.Close()
    }

    // close tun device (this will unblock read)
    if h.dev != nil {
        h.dev.Close()
    }

    h.started = false
}
