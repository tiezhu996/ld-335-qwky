package util

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// OK 统一成功响应 {code:0, message:"ok", data:...}。
func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": data})
}

// OKMessage 带自定义文案的成功响应（如冲正幂等命中时提示返回的是首次结果）。
func OKMessage(c *gin.Context, message string, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": message, "data": data})
}

// Created 创建成功响应。
func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, gin.H{"code": 0, "message": "ok", "data": data})
}

// PageData 分页数据。
type PageData struct {
	List  any   `json:"list"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"page_size"`
}

// Response 统一响应结构（Swagger 文档引用）。
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}
