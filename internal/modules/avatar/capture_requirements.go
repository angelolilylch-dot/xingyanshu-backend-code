package avatar

import "github.com/gin-gonic/gin"

const (
	consentAgreementVersion = "v1.0"
	photoMaxFileSize        = 8 * 1024 * 1024
	videoMaxFileSize        = 60 * 1024 * 1024
)

var requiredCaptureSlots = []gin.H{
	{"captureSlot": "FACE_01", "assetType": "FACE_PHOTO", "label": "正面平视", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_02", "assetType": "FACE_PHOTO", "label": "左转 45°", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_03", "assetType": "FACE_PHOTO", "label": "右转 45°", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_04", "assetType": "FACE_PHOTO", "label": "抬头", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_05", "assetType": "FACE_PHOTO", "label": "低头", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_06", "assetType": "FACE_PHOTO", "label": "左侧面", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_07", "assetType": "FACE_PHOTO", "label": "右侧面", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "FACE_08", "assetType": "FACE_PHOTO", "label": "自然微笑", "required": true, "maxFileSize": photoMaxFileSize, "mimeTypes": []string{"image/jpeg"}},
	{"captureSlot": "VOICE_VIDEO", "assetType": "VOICE_VIDEO", "label": "朗读视频", "required": true, "maxFileSize": videoMaxFileSize, "mimeTypes": []string{"video/mp4", "video/webm"}},
}

func requiredSlotsResponse() []gin.H {
	items := make([]gin.H, len(requiredCaptureSlots))
	copy(items, requiredCaptureSlots)
	return items
}
