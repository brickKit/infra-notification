// Package migrations 只做一件事：把这个目录下的 .sql 文件嵌进二进制，
// 供迁移入口（backend/cmd/migrate）交给 SDK 的 migrate.Main 执行——镜像
// 里不需要再带一份 migrations/ 目录。
//
// ⚠️ 不能反过来在别的包里 //go:embed ../../migrations——Go 的 embed 不
// 允许路径里出现 ".."，这是编译期硬限制。所以嵌入必须挨着这些 .sql 文件
// 本身的目录来做。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
