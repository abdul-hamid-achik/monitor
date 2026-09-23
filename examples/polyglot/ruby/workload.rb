#!/usr/bin/env ruby
require 'json'

START = Time.now
PID = Process.pid

def heavy_stringify(items)
  out = []
  items.each do |item|
    doubled = item * 2
    s = ""
    2500.times do |i|
      s += JSON.generate({ i: i, item: item, doubled: doubled, pad: "x" * 64 })  # HOT LINE (line 12)
    end
    out << s.length
  end
  out
end

def process_batch(n)
  items = (0...n).to_a
  heavy_stringify(items)
end

def flaky_parse(raw)
  raise "flaky_parse: malformed payload near token #{raw[0, 8]}" if raw.include?("bad")
  JSON.parse(raw)
end

def tick_errors
  ["{\"ok\":true}", "bad-payload-123", "{\"ok\":true}"].each do |p|
    begin
      flaky_parse(p)
    rescue => e
      warn "[ruby] caught in tick_errors: #{e.message}"
      warn e.backtrace.join("\n")
    end
  end
end

def detonate
  raise "ruby workload: intentional uncaught failure at t=#{(Time.now - START).round(1)}s"
end

warn "[ruby] pid=#{PID} workload starting"

hot_thread = Thread.new do
  loop do
    process_batch(40)
    sleep 0.04
  end
end

err_thread = Thread.new do
  loop do
    tick_errors
    sleep 4
  end
end

sleep 40
hot_thread.kill
err_thread.kill
detonate
