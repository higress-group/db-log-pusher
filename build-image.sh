#!/bin/bash
set -e

# 默认配置
PLUGIN_NAME="${PLUGIN_NAME:-db-log-pusher}"
REGISTRY="${REGISTRY:-}"
IMAGE_NAME="${IMAGE_NAME:-db-log-pusher}"
VERSION="${VERSION:-latest}"
PUSH="${PUSH:-false}"
PLATFORM="${PLATFORM:-linux/amd64,linux/arm64}"
WASM_GO_DIR="${WASM_GO_DIR:-$(cd "$(dirname "$0")/../.." && pwd)}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 打印信息函数
info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# 显示帮助信息
show_help() {
    cat << EOF
WASM插件OCI镜像打包脚本

用法: $0 [选项]

选项:
    -h, --help              显示帮助信息
    -n, --name NAME         插件名称 (默认: db-log-pusher)
    -r, --registry REGISTRY 镜像仓库地址 (例如: registry.example.com)
    -i, --image-name NAME   镜像名称 (默认: db-log-pusher)
    -v, --version VERSION   镜像版本 (默认: latest)
    -p, --push              构建后推送镜像到仓库
    --platform PLATFORM     目标平台 (默认: linux/amd64,linux/arm64)

示例:
    # 本地构建镜像
    $0

    # 构建并推送镜像到指定仓库
    $0 -r registry.example.com/myrepo -v 1.0.0 --push

    # 指定镜像名称和版本
    $0 -n my-plugin -i custom-name -v 2.0.0

EOF
}

# 解析命令行参数
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)
                show_help
                exit 0
                ;;
            -n|--name)
                PLUGIN_NAME="$2"
                shift 2
                ;;
            -r|--registry)
                REGISTRY="$2"
                shift 2
                ;;
            -i|--image-name)
                IMAGE_NAME="$2"
                shift 2
                ;;
            -v|--version)
                VERSION="$2"
                shift 2
                ;;
            -p|--push)
                PUSH="true"
                shift
                ;;
            --platform)
                PLATFORM="$2"
                shift 2
                ;;
            *)
                error "未知选项: $1"
                ;;
        esac
    done
}

# 检查依赖
check_dependencies() {
    info "检查依赖..."

    # 检查 Go
    if ! command -v go &> /dev/null; then
        error "未找到 Go，请先安装 Go 1.21 或更高版本"
    fi

    local go_version=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1)
    info "Go 版本: $go_version"

    # 检查 Docker
    if ! command -v docker &> /dev/null; then
        error "未找到 Docker，请先安装 Docker"
    fi

    # 检查 Docker buildx（用于多架构构建）
    if ! docker buildx version &> /dev/null; then
        warn "Docker buildx 未安装，将仅构建当前架构的镜像"
        PLATFORM=""
    fi

    info "依赖检查通过"
}

# 编译WASM
build_wasm() {
    info "开始编译 WASM..."

    # 清理旧的构建文件
    if [ -f "plugin.wasm" ]; then
        rm -f plugin.wasm
        info "清理旧的 plugin.wasm"
    fi

    export DOCKER_HOST=unix://$HOME/.lima/docker/sock/docker.sock 
    cd "$WASM_GO_DIR"
    if PLUGIN_NAME=new-db-log-pusher make build; then
        echo "✅ Docker 构建成功"
    fi

    cd extensions/new-db-log-pusher/

    if [ ! -f "plugin.wasm" ]; then
        error "WASM 编译失败，未生成 plugin.wasm"
    fi

    # 显示文件大小
    local wasm_size=$(du -h plugin.wasm | cut -f1)
    info "WASM 编译成功: plugin.wasm (${wasm_size})"
}

# 构建OCI镜像
build_image() {
    info "构建 OCI 镜像..."

    # 构建完整镜像标签
    local full_image_name
    if [ -n "$REGISTRY" ]; then
        full_image_name="${REGISTRY%/}/${IMAGE_NAME}:${VERSION}"
    else
        full_image_name="${IMAGE_NAME}:${VERSION}"
    fi

    info "镜像标签: $full_image_name"

    # 使用 buildx 进行多架构构建（如果支持）
    if [ -n "$PLATFORM" ] && docker buildx version &> /dev/null; then
        # 确保 buildx builder 存在
        if ! docker buildx inspect wasm-builder &> /dev/null; then
            info "创建 buildx builder: wasm-builder"
            docker buildx create --name wasm-builder --driver docker-container --use
        else
            docker buildx use wasm-builder
        fi

        local buildx_args="--platform ${PLATFORM}"
        if [ "$PUSH" = "true" ]; then
            buildx_args="${buildx_args} --push"
            info "构建并推送多架构镜像: $PLATFORM"
        else
            buildx_args="${buildx_args} --load"
            info "构建多架构镜像（仅加载到本地）: $PLATFORM"
        fi

        docker buildx build \
            ${buildx_args} \
            -f Dockerfile.wasm \
            -t "$full_image_name" \
            .
    else
        # 标准单架构构建
        docker build -f Dockerfile.wasm -t "$full_image_name" .

        if [ "$PUSH" = "true" ]; then
            info "推送镜像到仓库..."
            docker push "$full_image_name"
        fi
    fi

    info "镜像构建成功: $full_image_name"

    # 显示镜像信息
    if [ "$PUSH" != "true" ]; then
        info "镜像详情:"
        docker images "$full_image_name" --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}\t{{.CreatedAt}}"
    fi
}

# 推送镜像
push_image() {
    local full_image_name
    if [ -n "$REGISTRY" ]; then
        full_image_name="${REGISTRY%/}/${IMAGE_NAME}:${VERSION}"
    else
        full_image_name="${IMAGE_NAME}:${VERSION}"
    fi

    info "推送镜像: $full_image_name"
    docker push "$full_image_name"
    info "镜像推送成功"
}

# 清理构建文件
cleanup() {
    info "清理临时文件..."
    [ -f "Dockerfile.wasm" ] && rm -f Dockerfile.wasm
    [ -f "plugin.wasm" ] && rm -f plugin.wasm
    info "清理完成"
}

# 生成部署配置示例
show_deployment_example() {
    local full_image_name
    if [ -n "$REGISTRY" ]; then
        full_image_name="${REGISTRY%/}/${IMAGE_NAME}:${VERSION}"
    else
        full_image_name="${IMAGE_NAME}:${VERSION}"
    fi

    echo ""
    info "========== 部署配置示例 =========="
    cat << EOF

apiVersion: extensions.higress.io/v1alpha1
kind: WasmPlugin
metadata:
  name: ${PLUGIN_NAME}
  namespace: higress-system
spec:
  defaultConfig:
    collector_service_name: "db-log-collector"
    collector_port: 80
    collector_path: "/ingest"
  url: oci://${full_image_name}

EOF
    info "=================================="
}

# 主函数
main() {
    parse_args "$@"

    echo ""
    info "========== WASM OCI 镜像打包 =========="
    info "插件名称: $PLUGIN_NAME"
    info "镜像名称: $IMAGE_NAME"
    info "镜像版本: $VERSION"
    info "镜像仓库: ${REGISTRY:-<本地>}"
    info "推送镜像: $PUSH"
    [ -n "$PLATFORM" ] && info "目标平台: $PLATFORM"
    info "======================================"
    echo ""

    check_dependencies
    build_wasm
    build_image

    # 如果不推送，显示部署示例
    if [ "$PUSH" != "true" ]; then
        show_deployment_example
    fi

    cleanup

    echo ""
    info "打包完成！"
}

# 执行主函数
main "$@"
