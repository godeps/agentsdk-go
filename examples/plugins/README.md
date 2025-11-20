# Plugins Example

演示 `pkg/plugins` 的核心概念：`TrustStore`、`Manifest`、`LoadManifest` 与 `DiscoverManifests`。
示例会加载 `examples/plugins` 下的所有插件子目录，并打印签名校验后的清单信息。

## 运行

```bash
go run ./examples/plugins                # 默认扫描 examples/plugins
go run ./examples/plugins -allow-unsigned # 若需要允许未签名清单
```

## 示例插件

`sample-plugin` 的 `manifest.yaml` 已使用 `example-dev` 的 Ed25519 公钥签名，公钥（十六进制）在 `main.go` 中注册。
入口脚本 `plugin.sh` 只是回显传入参数，便于快速验证 `Digest` 与 `EntrypointAbs`。

目录结构：

- `main.go`：注册 `TrustStore`，使用 `LoadManifest` 加载单个插件，再通过 `DiscoverManifests` 扫描整个目录。
- `sample-plugin/manifest.yaml`：声明名称、版本、入口脚本、能力、元数据、签名与摘要。
- `sample-plugin/plugin.sh`：示例入口脚本，sha256 与签名与清单保持一致。
