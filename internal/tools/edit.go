package tools

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/reuben/go-harness/internal/types"
)

func EditTool(cwd string) *types.Tool {
	return &types.Tool{
		Name:    "edit",
		Snippet: "改文件里的一小段，一次可以改多处",
		Desc:    "改一个文件里的一小段，按原文精确替换。oldText 必须和文件里的内容一模一样、而且在文件里只出现一次；一次调用可以带多条 edits，但都按原文件定位，互相不能重叠。",
		Args: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "要改的文件路径。相对路径按工作目录解析，~ 展开成家目录。",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "要改的地方，至少一处。每条都按原文件定位，不是上一条改完再算下一条。不要给重叠或嵌套的编辑，两处改动挨着就合并成一条。",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"oldText": map[string]any{
								"type":        "string",
								"description": "要被替换掉的原内容，必须和原文件里的一模一样（包括空格和换行），而且在文件里只能出现一次。",
							},
							"newText": map[string]any{
								"type":        "string",
								"description": "替换后的新内容，原内容不要了可以给空字符串",
							},
						},
						"required": []string{
							"oldText",
							"newText",
						},
					},
				},
			},
			"required": []string{
				"path",
				"edits",
			},
		},
		Guidelines: []string{
			"改之前先 read 一遍，oldText 照着文件里的原文抄，空格和换行都别自己补。",
			"同一个文件的多处改动放在一次 edit 里给，不要分几次调用。",
			"每条 oldText 都按原文件定位，不是按上一条改完的结果算；区域之间不能重叠，挨得近的改动合并成一条。",
			"oldText 够唯一就行，别为了把相距很远的两处改动串起来而把中间没动的部分也抄进去。",
		},
		Execute: func(ctx context.Context, args map[string]any, _ types.SendPartialResult) (types.ToolResult, error) {
			return executeEdit(ctx, cwd, args)
		},
	}
}

func executeEdit(ctx context.Context, cwd string, args map[string]any) (types.ToolResult, error) {
	rawPath := strings.TrimSpace(stringArg(args, "path"))
	if rawPath == "" {
		return types.ToolResult{}, fmt.Errorf("缺少 path 参数")
	}

	path, err := resolvePath(cwd, rawPath)
	if err != nil {
		return types.ToolResult{}, err
	}

	edits, err := parseEdits(args["edits"])
	if err != nil {
		return types.ToolResult{}, err
	}

	if err := ctx.Err(); err != nil {
		return types.ToolResult{}, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return types.ToolResult{}, err
	}

	const bomMark = "\uFEFF"
	bom := ""
	if text, ok := strings.CutPrefix(string(raw), bomMark); ok {
		bom, raw = bomMark, []byte(text)
	}

	content := normalizeLF(string(raw))
	// 记录文件结尾是 "\n" 还是 "\r\n" 还是 "\r"，用于最后替换回来
	ending := recordEnding(string(raw))

	places, err := locateEdits(content, edits)
	if err != nil {
		return types.ToolResult{}, err
	}

	// 倒序 先改后面的内容不会影响前面的 index 准确性
	for _, p := range slices.Backward(places) {
		content = content[:p.index] + p.newText + content[p.index+len(p.oldText):]
	}

	// 拼接 bom 以及将不同 os 换行符换回
	content = bom + restoreLineBreaks(content, ending)

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return types.ToolResult{}, err
	}

	return textResult(fmt.Sprintf("已修改 %s，替换了 %d 处", path, len(places))), nil
}

type replacement struct {
	no      int // 在用户给的 edits 里排第几处，从 1 数起，只用来报错
	index   int
	oldText string
	newText string
}

func parseEdits(raw any) ([]replacement, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("edits 必须是 array 类型")
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("edits 至少要有一处")
	}

	edits := make([]replacement, 0, len(list))
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits 第 %d 处不是一个对象", i+1)
		}
		oldText, _ := m["oldText"].(string)
		newText, _ := m["newText"].(string)
		if oldText == "" {
			return nil, fmt.Errorf("edits 第 %d 处 oldText 为空", i+1)
		}
		// newText 也要归一化：留给 restoreLineBreaks 统一换回去，不然 CRLF 文件里会插出 \r\r\n
		edits = append(edits, replacement{no: i + 1, oldText: normalizeLF(oldText), newText: normalizeLF(newText)})
	}

	return edits, nil
}

// locateEdits 找出编辑区域的 index，重复/重叠 报错
func locateEdits(content string, edits []replacement) ([]replacement, error) {
	places := make([]replacement, 0, len(edits))
	for i, edit := range edits {
		at := findAllIdx(content, edit.oldText)
		switch len(at) {
		case 0:
			return nil, fmt.Errorf("第 %d 处的要编辑的内容 %s 在文件里找不到，可以用 read 工具进行确认", i+1, edit.oldText)
		case 1:
			edit.index = at[0]
			places = append(places, edit)
		default:
			return nil, fmt.Errorf("第 %d 处的要编辑的内容 %s 在文件中出现了 %d 次，可以尝试提供更多上下行信息增大唯一可能性以减少这种情况",
				i+1, edit.oldText, len(at))
		}
	}

	slices.SortFunc(places, func(a, b replacement) int {
		return cmp.Compare(a.index, b.index)
	})
	for i := 1; i < len(places); i++ {
		prev := places[i-1]
		if places[i].index < prev.index+len(prev.oldText) {
			// 排序之后下标已经不是用户给的顺序了，报错要用 no
			return nil, fmt.Errorf("第 %d 处和第 %d 处要编辑的内容有部分重叠了，合并成一条再试", prev.no, places[i].no)
		}
	}
	return places, nil
}

func findAllIdx(content, target string) []int {
	at := make([]int, 0)
	for from := 0; from <= len(content)-len(target); {
		found := strings.Index(content[from:], target)
		if found < 0 {
			break
		}
		at = append(at, from+found)
		// 从这次命中的末尾继续，重叠的命中不重复算
		from += found + len(target)
	}
	return at
}
