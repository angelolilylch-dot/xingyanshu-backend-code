# 星衍树后端

## 本地测试环境

先在 `config/test.yaml` 中填写 MySQL、`bootstrap.initial_admin` 和本地测试信息，再依次执行：

个人形象 H5 需要绕过 COS 浏览器跨域时，可只在 `avatar.asset_storage` 配置 Mock；它不会影响后台通用文件继续使用 `file.storage: cos`。该 Mock 仅允许 `app.environment: test`，且 `mock_upload_base_url` 必须填写浏览器可访问的后端地址。

```powershell
.\scripts\use-go125.ps1
go build -o .\bin\xingyanshu.exe .\cmd\xingyanshu
.\bin\xingyanshu.exe migrate -config .\config\test.yaml
.\bin\xingyanshu.exe bootstrap -config .\config\test.yaml
```

`migrate` 只执行未登记的 SQL 迁移，并校验已执行文件的 SHA256；已执行迁移不得修改。

`bootstrap` 在迁移完成后创建或复用平台根组织、7 个预置后台角色、职责级权限及首个平台管理员。它以组织编码、角色编码、权限编码和管理员账号为幂等键，可重复执行；管理员账号已存在时不会覆盖人工修改后的密码或资料。

测试配置中的管理员密码仅用于本地开发。生产环境必须使用服务器私有配置文件，并在初始化后立即替换初始密码。

采集提交后可选接入腾讯云 `ImageRecognitionV2` 进行正面照本人一致性核验。配置 `avatar.face_identity.provider=tencent_image_recognition_v2` 后，进程必须提供 `XINGYANSHU_FACEID_SECRET_ID`、`XINGYANSHU_FACEID_SECRET_KEY`、`XINGYANSHU_FACEID_RSA_PUBLIC_KEY`，并在 `XINGYANSHU_FACEID_PROVIDER` 填写 `tencent_image_recognition_v2`；不得把这些值写入 YAML、Git、日志或文档。未配置时不创建该异步人脸核验任务。

测试与生产均应使用服务器私有配置或运行进程环境变量保存第三方凭据。Bucket 地址必须是完整 HTTPS 地址，例如 `https://bucket-appid.cos.ap-guangzhou.myqcloud.com`。
