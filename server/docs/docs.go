// Package docs GENERATED BY THE COMMAND ABOVE; DO NOT EDIT
// This file was generated by swaggo/swag
package docs

import (
	"bytes"
	"encoding/json"
	"strings"
	"text/template"

	"github.com/swaggo/swag"
)

var doc = `{
    "schemes": {{ marshal .Schemes }},
    "swagger": "2.0",
    "info": {
        "description": "{{escape .Description}}",
        "title": "{{.Title}}",
        "contact": {},
        "version": "{{.Version}}"
    },
    "host": "{{.Host}}",
    "basePath": "{{.BasePath}}",
    "paths": {
        "/sm/server/add-shard": {
            "post": {
                "description": "增加shard",
                "consumes": [
                    "application/json"
                ],
                "produces": [
                    "application/json"
                ],
                "tags": [
                    "shard管理"
                ],
                "parameters": [
                    {
                        "description": "param",
                        "name": "param",
                        "in": "body",
                        "required": true,
                        "schema": {
                            "$ref": "#/definitions/smserver.addShardRequest"
                        }
                    }
                ],
                "responses": {
                    "200": {
                        "description": ""
                    }
                }
            }
        },
        "/sm/server/add-spec": {
            "post": {
                "description": "增加spec",
                "consumes": [
                    "application/json"
                ],
                "produces": [
                    "application/json"
                ],
                "tags": [
                    "spec管理"
                ],
                "parameters": [
                    {
                        "description": "param",
                        "name": "param",
                        "in": "body",
                        "required": true,
                        "schema": {
                            "$ref": "#/definitions/smserver.smAppSpec"
                        }
                    }
                ],
                "responses": {
                    "200": {
                        "description": ""
                    }
                }
            }
        },
        "/sm/server/del-shard": {
            "post": {
                "description": "删除shard",
                "consumes": [
                    "application/json"
                ],
                "produces": [
                    "application/json"
                ],
                "tags": [
                    "shard管理"
                ],
                "parameters": [
                    {
                        "description": "param",
                        "name": "param",
                        "in": "body",
                        "required": true,
                        "schema": {
                            "$ref": "#/definitions/smserver.delShardRequest"
                        }
                    }
                ],
                "responses": {
                    "200": {
                        "description": ""
                    }
                }
            }
        },
        "/sm/server/del-spec": {
            "get": {
                "description": "删除spec",
                "consumes": [
                    "application/json"
                ],
                "produces": [
                    "application/json"
                ],
                "tags": [
                    "spec管理"
                ],
                "parameters": [
                    {
                        "type": "string",
                        "description": "param",
                        "name": "service",
                        "in": "query",
                        "required": true
                    }
                ],
                "responses": {
                    "200": {
                        "description": ""
                    }
                }
            }
        }
    },
    "definitions": {
        "smserver.addShardRequest": {
            "type": "object",
            "required": [
                "service",
                "shardId",
                "task"
            ],
            "properties": {
                "group": {
                    "description": "Group 同一个service需要区分不同种类的shard，这些shard之间不相关的balance到现有container上",
                    "type": "string"
                },
                "manualContainerId": {
                    "type": "string"
                },
                "service": {
                    "description": "为哪个业务app增加shard",
                    "type": "string"
                },
                "shardId": {
                    "type": "string"
                },
                "task": {
                    "description": "业务app自己定义task内容",
                    "type": "string"
                }
            }
        },
        "smserver.delShardRequest": {
            "type": "object",
            "required": [
                "service",
                "shardId"
            ],
            "properties": {
                "service": {
                    "type": "string"
                },
                "shardId": {
                    "type": "string"
                }
            }
        },
        "smserver.smAppSpec": {
            "type": "object",
            "required": [
                "service"
            ],
            "properties": {
                "createTime": {
                    "type": "integer"
                },
                "maxRecoveryTime": {
                    "description": "MaxRecoveryTime 遇到container删除的场景，等待的时间，超时认为该container被清理",
                    "type": "integer"
                },
                "maxShardCount": {
                    "description": "MaxShardCount 单container承载的最大分片数量，防止雪崩",
                    "type": "integer"
                },
                "service": {
                    "description": "Service 目前app的spec更多承担的是管理职能，shard配置的一个起点，先只配置上service，可以唯一标记一个app",
                    "type": "string"
                }
            }
        }
    }
}`

type swaggerInfo struct {
	Version     string
	Host        string
	BasePath    string
	Schemes     []string
	Title       string
	Description string
}

// SwaggerInfo holds exported Swagger Info so clients can modify it
var SwaggerInfo = swaggerInfo{
	Version:     "",
	Host:        "",
	BasePath:    "",
	Schemes:     []string{},
	Title:       "",
	Description: "",
}

type s struct{}

func (s *s) ReadDoc() string {
	sInfo := SwaggerInfo
	sInfo.Description = strings.Replace(sInfo.Description, "\n", "\\n", -1)

	t, err := template.New("swagger_info").Funcs(template.FuncMap{
		"marshal": func(v interface{}) string {
			a, _ := json.Marshal(v)
			return string(a)
		},
		"escape": func(v interface{}) string {
			// escape tabs
			str := strings.Replace(v.(string), "\t", "\\t", -1)
			// replace " with \", and if that results in \\", replace that with \\\"
			str = strings.Replace(str, "\"", "\\\"", -1)
			return strings.Replace(str, "\\\\\"", "\\\\\\\"", -1)
		},
	}).Parse(doc)
	if err != nil {
		return doc
	}

	var tpl bytes.Buffer
	if err := t.Execute(&tpl, sInfo); err != nil {
		return doc
	}

	return tpl.String()
}

func init() {
	swag.Register("swagger", &s{})
}
