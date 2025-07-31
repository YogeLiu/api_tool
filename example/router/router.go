package router

import "github.com/gin-gonic/gin"

func InitRouter(r *gin.Engine) {
	user := r.Group("/user")
	{
		user.GET("/info", GetUserInfo)
		user.GET("/book", BookInfo)
		user.GET("/users", GetUsers)
	}
}
