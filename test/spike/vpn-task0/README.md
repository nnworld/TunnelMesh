# VPN 网关 Task 0 可行性 spike（一次性产物）

本目录是一次性技术验证代码，**不是产品代码**，不被主模块引用（独立 `go.mod`）。
结论汇总见 `REPORT.md`，设计依据见
`docs/superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md` 第 2.2 节。

依赖版本是**刻意钉死**的：`gvisor.dev/gvisor@latest` 无法被标准 Go 工具链导入
（`pkg/tcpip/stack/bridge_test.go` 声明了非法的 `package bridge_test`），必须使用
wireguard-go 自身 pin 的 `v0.0.0-20250503011706-39ed1f5ac29c`。不要执行
`go get -u`，那会把 gVisor 抬回不可构建的 revision。

运行全部探针（`icmpsock` 需 Linux，其余平台输出 SKIP）：

    cd test/spike/vpn-task0
    for p in deps tcpintercept udpintercept wgbridge icmpsock; do
      go run ./$p; echo "$p exit=$?"
    done
