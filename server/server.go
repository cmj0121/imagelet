// Package server builds the gin engine for the imagelet HTTP service.
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter returns a gin engine with the imagelet routes registered.
func NewRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/", rootHandler)
	return r
}

// rootHandler responds with HTTP 200 and an empty body.
func rootHandler(c *gin.Context) {
	c.Status(http.StatusOK)
}
