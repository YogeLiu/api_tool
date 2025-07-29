import json


def analyze_path_duplicates(data):
    """
    检查路径是否有重复（仅当 handler 相同时）：
    1. 完全相等
    2. 一个是另一个的后缀（尾部完全相同）
    且：两个路由的 handler 必须相同
    """
    routes = data.get("routes", [])

    # 存储每个路由的信息：path, segments, handler
    route_list = []
    for route in routes:
        path = route["path"]
        handler = route.get("handler", "")
        segments = [s for s in path.strip("/").split("/") if s]
        route_list.append({"path": path, "segments": segments, "handler": handler})

    duplicates = []  # 存储重复对

    n = len(route_list)
    for i in range(n):
        for j in range(i + 1, n):
            r1 = route_list[i]
            r2 = route_list[j]

            # 只有当 handler 相同时才判断路径重复
            if r1["handler"] != r2["handler"]:
                continue

            seg1, seg2 = r1["segments"], r2["segments"]
            path1, path2 = r1["path"], r2["path"]

            if seg1 == seg2:
                # 完全相等
                duplicates.append((path1, path2, "完全相等", r1["handler"]))
            elif len(seg1) > len(seg2) and seg1[-len(seg2) :] == seg2:
                # seg2 是 seg1 的后缀
                duplicates.append((path1, path2, f"后缀匹配: {path2} 是 {path1} 的尾部", r1["handler"]))
            elif len(seg2) > len(seg1) and seg2[-len(seg1) :] == seg1:
                # seg1 是 seg2 的后缀
                duplicates.append((path1, path2, f"后缀匹配: {path1} 是 {path2} 的尾部", r1["handler"]))

    return duplicates


def analyze_paths_per_handler(data, min_count=1):
    """
    统计每个 handler 对应的 path 个数
    :param data: 路由数据
    :param min_count: 只返回 path 个数 >= min_count 的 handler（默认 1 表示返回全部）
    :return: 字典，key=handler, value={count: 数量, paths: path 列表}
    """
    routes = data.get("routes", [])
    handler_map = {}

    for route in routes:
        path = route["path"]
        handler = route.get("handler", "")  # 如果没有 handler 字段，默认为空字符串

        if handler not in handler_map:
            handler_map[handler] = {"paths": [], "count": 0}

        # 避免同一个 path 被重复添加（虽然一般不会）
        if path not in handler_map[handler]["paths"]:
            handler_map[handler]["paths"].append(path)
            handler_map[handler]["count"] += 1

    # 过滤：只保留 count >= min_count 的
    result = {handler: info for handler, info in handler_map.items() if info["count"] >= min_count}

    # 按 count 降序排列，便于查看“绑定最多路径”的 handler
    sorted_result = dict(sorted(result.items(), key=lambda x: x[1]["count"], reverse=True))

    with open("handler_paths.json", "w", encoding="utf-8") as f:
        json.dump(sorted_result, f, indent=4, ensure_ascii=False)


def write_sample_data(duplicates):
    """
    将 duplicates 转为 KV 结构并写入 JSON 文件
    """
    result = {}
    for idx, (path1, path2, reason, handler) in enumerate(duplicates, start=1):
        key = f"duplicate_{idx}"
        result[key] = {"path1": path1, "path2": path2, "reason": reason, "handler": handler}  # 保留 handler 信息

    with open("sample_data.json", "w", encoding="utf-8") as f:
        json.dump(result, f, indent=4, ensure_ascii=False)
    print("重复路径（相同 handler）已序列化并保存到 sample_data.json")


if __name__ == "__main__":
    with open("scotty_api.json", "r", encoding="utf-8") as f:
        data = json.load(f)

    # duplicates = analyze_path_duplicates(data)

    # if duplicates:
    #     write_sample_data(duplicates)
    # else:
    #     print("未发现相同 handler 的重复路径。")

    analyze_paths_per_handler(data)
