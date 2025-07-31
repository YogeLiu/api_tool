package router

import (
	"strconv"

	"github.com/YogeLiu/api-tool/example/sevice"
	"github.com/gin-gonic/gin"
)

type BookInfoDTO struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type BookInfoReq struct {
	BookID int `json:"book_id"`
}

func GetUserInfo(c *gin.Context) {

	var req sevice.UserInfoReq
	if err := c.Bind(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	user := sevice.UserInfo{
		ID:   req.UserID,
		Name: "test",
	}
	c.JSON(200, sevice.ResponseOK(c, user))
}

func BookInfo(c *gin.Context) {
	var req BookInfoReq
	if err := c.ShouldBindUri(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	book := BookInfoDTO{
		ID:   req.BookID,
		Name: "test",
	}

	sevice.APIResponseOK(c, book)
}

func GetUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	pageSize, _ := strconv.Atoi(c.Query("page_size"))

	users := []sevice.UserInfo{
		{ID: 1, Name: "test"},
		{ID: 2, Name: "test2"},
	}

	c.JSON(200, sevice.ResponseData(c, users, "success", page*pageSize))
}
