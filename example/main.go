package main

import (
	"github.com/YogeLiu/api-tool/example/router"
	"github.com/gin-gonic/gin"
)

func main() {
	engine := gin.Default()

	engine.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "pong"})
	})

	router.InitRouter(engine)

	engine.Run(":8080")
}
