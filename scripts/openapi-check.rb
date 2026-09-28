#!/usr/bin/env ruby
# openapi-check.rb — MockNetPack openapi 契约校验（A8：ruby 校验）。
# 用法：ruby scripts/openapi-check.rb [path-to-mocknetpack.yaml]
# 校验：YAML 语法（Psych）+ OpenAPI 结构 + 关键契约点（版本号、traffic 过滤参数、
# CaptureSession.retainUntil）。任何不满足即非零退出。

require "yaml"

path = ARGV[0] || File.join(__dir__, "..", "openapi", "mocknetpack.yaml")
doc = YAML.load_file(path)
raise "not a Hash" unless doc.is_a?(Hash)

%w[openapi info paths].each { |k| raise "missing top-level #{k}" unless doc.key?(k) }
raise "version != 0.10.0 (got #{doc.dig("info", "version").inspect})" unless doc.dig("info", "version") == "0.10.0"
raise "title missing" if doc.dig("info", "title").to_s.empty?

paths = doc["paths"]
raise "no paths" if paths.nil? || paths.empty?

bad = []
paths.each do |p, item|
  next unless item.is_a?(Hash)
  item.each do |meth, op|
    next unless %w[get post put delete patch].include?(meth)
    bad << "#{meth.upcase} #{p}: no responses" unless op.is_a?(Hash) && op["responses"].is_a?(Hash) && !op["responses"].empty?
    bad << "#{meth.upcase} #{p}: no summary" if op["summary"].to_s.empty?
  end
end
raise "structure problems: #{bad.join("; ")}" unless bad.empty?

tl = paths.dig("/sessions/{id}/traffic", "get", "parameters")
raise "missing traffic list parameters" unless tl.is_a?(Array)
names = tl.map { |x| x["name"] }
%w[keyword statusCode from to limit offset].each { |n| raise "traffic list missing #{n}" unless names.include?(n) }

cs = doc.dig("components", "schemas", "CaptureSession", "properties")
raise "CaptureSession missing retainUntil" unless cs && cs.key?("retainUntil")

puts "OK: openapi v#{doc.dig("info", "version")}, #{paths.size} paths, ruby YAML + structure checks passed"
