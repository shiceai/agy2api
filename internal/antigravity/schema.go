package antigravity

import "strings"

// sanitizeTools 归一 request.tools：每个声明的参数 schema 统一为 parametersJsonSchema，
// 并清洗成 Cloud Code 前端接受的形态（内联 $ref、展平 anyOf/oneOf/allOf、去掉不支持的关键字）。
func sanitizeTools(inner map[string]any) {
	tools, ok := inner["tools"].([]any)
	if !ok || len(tools) == 0 {
		return
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			decls, ok := tool[key].([]any)
			if !ok {
				continue
			}
			for _, rawDecl := range decls {
				decl, ok := rawDecl.(map[string]any)
				if !ok {
					continue
				}
				applyDeclarationSchema(decl)
			}
			if key == "function_declarations" {
				tool["functionDeclarations"] = decls
				delete(tool, "function_declarations")
			}
		}
	}
}

// applyDeclarationSchema 把一个函数声明的参数 schema 清洗后写回 parametersJsonSchema。
func applyDeclarationSchema(decl map[string]any) {
	raw := decl["parameters"]
	if raw == nil {
		raw = decl["parametersJsonSchema"]
	}
	if raw == nil {
		raw = decl["parameters_json_schema"]
	}
	delete(decl, "parameters")
	delete(decl, "parameters_json_schema")
	if raw == nil {
		return
	}
	root, _ := raw.(map[string]any)
	defs := collectDefs(root)
	cleaned := cleanSchema(raw, defs, map[string]bool{})
	decl["parametersJsonSchema"] = cleaned
}

func collectDefs(root map[string]any) map[string]any {
	defs := map[string]any{}
	if root == nil {
		return defs
	}
	for _, key := range []string{"$defs", "definitions"} {
		if m, ok := root[key].(map[string]any); ok {
			for name, def := range m {
				defs[name] = def
			}
		}
	}
	return defs
}

// cleanSchema 递归清洗 JSON Schema：内联本地 $ref、展平联合、去掉 Cloud Code 不认的关键字。
func cleanSchema(value any, defs map[string]any, visiting map[string]bool) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, cleanSchema(item, defs, visiting))
		}
		return out
	case map[string]any:
		return cleanSchemaObject(typed, defs, visiting)
	default:
		return value
	}
}

func cleanSchemaObject(obj map[string]any, defs map[string]any, visiting map[string]bool) any {
	// 内联本地 $ref。
	if ref, ok := stringField(obj, "$ref"); ok {
		name := refName(ref)
		if name != "" {
			if visiting[name] {
				return map[string]any{"type": "object", "description": "circular reference"}
			}
			if target, ok := defs[name].(map[string]any); ok {
				visiting[name] = true
				merged := deepCopyMap(target)
				// 保留引用点上的 description / default 覆盖。
				for _, k := range []string{"description", "default"} {
					if v, ok := obj[k]; ok {
						merged[k] = v
					}
				}
				result := cleanSchemaObject(merged, defs, visiting)
				delete(visiting, name)
				return result
			}
		}
	}

	// 展平 allOf：合并到当前对象。
	if allOf, ok := obj["allOf"].([]any); ok {
		for _, branch := range allOf {
			if bm, ok := cleanSchema(branch, defs, visiting).(map[string]any); ok {
				mergeSchema(obj, bm)
			}
		}
		delete(obj, "allOf")
	}

	// 展平 anyOf / oneOf：挑选信息量最高的分支，记录是否可空。
	for _, unionKey := range []string{"anyOf", "oneOf"} {
		branches, ok := obj[unionKey].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		hasNull := false
		var best map[string]any
		bestScore := -1
		for _, branch := range branches {
			bm, ok := cleanSchema(branch, defs, visiting).(map[string]any)
			if !ok {
				continue
			}
			if t, _ := stringField(bm, "type"); strings.EqualFold(t, "null") {
				hasNull = true
				continue
			}
			if sc := branchScore(bm); sc > bestScore {
				bestScore, best = sc, bm
			}
		}
		if best != nil {
			mergeSchema(obj, best)
		}
		if hasNull {
			obj["nullable"] = true
		}
		delete(obj, unionKey)
	}

	// 去掉 Cloud Code 不支持的关键字。
	for _, k := range []string{"$schema", "$id", "$defs", "definitions", "$ref",
		"additionalProperties", "patternProperties", "unevaluatedProperties"} {
		delete(obj, k)
	}

	// 递归子节点。
	for k, v := range obj {
		switch k {
		case "enum", "const", "default", "example", "examples":
			// 字面量数据，原样保留。
		default:
			obj[k] = cleanSchema(v, defs, visiting)
		}
	}
	return obj
}

func mergeSchema(dst, src map[string]any) {
	if srcProps, ok := src["properties"].(map[string]any); ok {
		dstProps, _ := dst["properties"].(map[string]any)
		if dstProps == nil {
			dstProps = map[string]any{}
			dst["properties"] = dstProps
		}
		for k, v := range srcProps {
			if _, exists := dstProps[k]; !exists {
				dstProps[k] = v
			}
		}
	}
	if srcReq, ok := src["required"].([]any); ok {
		seen := map[string]bool{}
		dstReq, _ := dst["required"].([]any)
		for _, r := range dstReq {
			if name, ok := r.(string); ok {
				seen[name] = true
			}
		}
		for _, r := range srcReq {
			if name, ok := r.(string); ok && !seen[name] {
				dstReq = append(dstReq, name)
				seen[name] = true
			}
		}
		if len(dstReq) > 0 {
			dst["required"] = dstReq
		}
	}
	if t, ok := stringField(src, "type"); ok {
		if _, exists := dst["type"]; !exists {
			dst["type"] = t
		}
	}
	for k, v := range src {
		switch k {
		case "properties", "required", "type":
		default:
			if _, exists := dst[k]; !exists {
				dst[k] = v
			}
		}
	}
}

func branchScore(schema map[string]any) int {
	if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
		return 3
	}
	if t, _ := stringField(schema, "type"); strings.EqualFold(t, "object") {
		return 3
	}
	if _, ok := schema["items"]; ok {
		return 2
	}
	if t, _ := stringField(schema, "type"); t != "" && !strings.EqualFold(t, "null") {
		return 1
	}
	return 0
}

func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key].(string)
	return v, ok
}

func refName(ref string) string {
	ref = strings.TrimSpace(ref)
	for _, prefix := range []string{"#/$defs/", "#/definitions/"} {
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
	}
	return ""
}
