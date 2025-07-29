package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	r := gin.New()

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})

	r.POST("/users", func(c *gin.Context) {
		var user User
		c.ShouldBindJSON(&user)
		c.JSON(http.StatusOK, user)
	})

	api := r.Group("/api/v1")
	{
		api.GET("/users/:id", func(c *gin.Context) {
			id := c.Param("id")
			name := c.Query("name")
			c.JSON(http.StatusOK, gin.H{"id": id, "name": name})
		})
	}

	r.Run(":8080")
}
