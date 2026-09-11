# 星衍树后端

## 本地测试环境

先在 `config/test.yaml` 中填写 MySQL、`bootstrap.initial_admin` 和 COS 环境变量的本地测试信息，再依次执行：

```powershell
.\scripts\use-go125.ps1
go build -o .\bin\xingyanshu.exe .\cmd\xingyanshu
.\bin\xingyanshu.exe migrate -config .\config\test.yaml
.\bin\xingyanshu.exe bootstrap -config .\config\test.yaml
```

`migrate` 只执行未登记的 SQL 迁移，并校验已执行文件的 SHA256；已执行迁移不得修改。

`bootstrap` 在迁移完成后创建或复用平台根组织、7 个预置后台角色、职责级权限及首个平台管理员。它以组织编码、角色编码、权限编码和管理员账号为幂等键，可重复执行；管理员账号已存在时不会覆盖人工修改后的密码或资料。

测试配置中的管理员密码仅用于本地开发。生产环境必须使用服务器私有配置文件，并在初始化后立即替换初始密码。

测试配置已直接写入 COS 测试桶地址和测试凭据，可直接启动。此配置仅用于测试，不得复制为生产配置或提交到公开仓库；生产环境仍应使用服务器私有配置文件或环境变量保存 COS 凭据。Bucket 地址必须是完整 HTTPS 地址，例如 `https://bucket-appid.cos.ap-guangzhou.myqcloud.com`。
