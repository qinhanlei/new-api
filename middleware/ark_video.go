package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
)

// PrepareArkVideo translates the native Ark body to the existing shared video
// protocol. The source fields stay in metadata, including explicit false/zero.
func PrepareArkVideo() gin.HandlerFunc {
	return func(c *gin.Context) {
		var body map[string]any
		if err := common.UnmarshalBodyReusable(c, &body); err != nil || body == nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgVideoJSONRequired))
			return
		}
		content, ok := body["content"].([]any)
		if !ok || len(content) == 0 {
			abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgVideoContentRequired))
			return
		}
		texts := make([]string, 0)
		for _, item := range content {
			entry, ok := item.(map[string]any)
			if !ok {
				abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgVideoContentEntryInvalid))
				return
			}
			if entry["type"] == "text" {
				text, ok := entry["text"].(string)
				if !ok {
					abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgVideoContentTextInvalid))
					return
				}
				texts = append(texts, text)
			}
		}
		canonical := map[string]any{"model": body["model"], "prompt": strings.Join(texts, "\n"), "metadata": body}
		delete(body, "model")
		if duration, exists := body["duration"]; exists {
			canonical["seconds"] = duration
		}
		encoded, err := common.Marshal(canonical)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgVideoContentInvalid))
			return
		}
		storage, err := common.CreateBodyStorage(encoded)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusInternalServerError, i18n.T(c, i18n.MsgVideoReadFailed))
			return
		}
		common.CleanupBodyStorage(c)
		c.Set(common.KeyBodyStorage, storage)
		c.Request.ContentLength = int64(len(encoded))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("ark_video", true)
		c.Next()
	}
}
