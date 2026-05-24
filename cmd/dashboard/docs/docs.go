package docs

import "github.com/swaggo/swag"

var SwaggerInfo = swag.Spec{
	Version:          "1.0",
	Host:             "localhost:8008",
	BasePath:         "/api/v1",
	Schemes:          []string{"http", "https"},
	Title:            "Nezha Monitoring API",
	Description:      "Nezha Monitoring API",
	InfoInstanceName: "swagger",
	SwaggerTemplate:  "",
}
