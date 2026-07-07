package controller

import (
	"testing"

	"github.com/nezhahq/nezha/model"
	"github.com/nezhahq/nezha/service/singleton"
)

func TestAIAllowedToolDefsFiltersByConfig(t *testing.T) {
	all := aiAllowedToolDefs(&singleton.ConfigClass{Config: &model.Config{}})
	if len(all) == 0 {
		t.Fatal("expected all tools when allowlist empty")
	}

	conf := &singleton.ConfigClass{Config: &model.Config{AIAllowedTools: "list_servers,get_server_metrics"}}
	allowed := aiAllowedToolDefs(conf)
	if len(allowed) != 2 {
		t.Fatalf("expected 2 allowed tools, got %d", len(allowed))
	}
	for _, d := range allowed {
		if d.Function.Name != "list_servers" && d.Function.Name != "get_server_metrics" {
			t.Fatalf("unexpected tool in allowlist: %s", d.Function.Name)
		}
	}

	// 未知工具名应被忽略，不产出多余条目。
	conf2 := &singleton.ConfigClass{Config: &model.Config{AIAllowedTools: "list_servers,does_not_exist"}}
	if got := len(aiAllowedToolDefs(conf2)); got != 1 {
		t.Fatalf("expected 1 tool after dropping unknown, got %d", got)
	}
}

func TestToFloat64ID(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{float64(3), 3},
		{int(7), 7},
		{int64(9), 9},
		{"42", 42},
		{"12.5", 12.5},
		{"notanumber", 0},
	}
	for _, c := range cases {
		if got := toFloat64ID(c.in); got != c.want {
			t.Fatalf("toFloat64ID(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
