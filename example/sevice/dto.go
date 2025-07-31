package sevice

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Response struct {
	RequestID string      `json:"request_id"`
	Code      int         `json:"code"`
	Message   string      `json:"message"`
	Data      interface{} `json:"data"`
}

func ResponseOK(ctx context.Context, data interface{}) *Response {
	return &Response{
		RequestID: "req-123",
		Code:      200,
		Message:   "success",
		Data:      data,
	}
}

func APIResponseOK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, ResponseOK(c, data))
}

func ResponseData(c *gin.Context, data interface{}, message string, next int) gin.H {
	return gin.H{`code`: 0, `data`: data, `message`: message, `next`: next}
}

type UserInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type UserInfoReq struct {
	UserID int `json:"user_id"`
}
