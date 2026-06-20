# Runtime only — 二进制预编译为 linux 静态文件
FROM harbor.isvbytes.com/library/python:3.12-slim

# 使用静态二进制（无需 Python 运行时，但 python:3.12-slim 是 Harbor 已有的最轻量镜像）
COPY codex-relayx-linux /usr/local/bin/codex-relayx
RUN chmod +x /usr/local/bin/codex-relayx

# 数据目录（config.json、日志）
RUN mkdir -p /var/lib/codex-relayx
ENV RELAYX_DATA_DIR=/var/lib/codex-relayx

EXPOSE 8001

ENTRYPOINT ["/usr/local/bin/codex-relayx"]
CMD ["--port", "8001", "--data-dir", "/var/lib/codex-relayx"]
