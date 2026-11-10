-- dbv.lua: run the SQL statement under the cursor, or the selection, and
-- see its rows in dbv's grid in a split below (dbv show -h). :DBVRun runs
-- the statement at the cursor; from visual mode (:'<,'>DBVRun) the
-- selection, a block (Ctrl-V) too; with a range of lines, those lines. :DBVInfo says which
-- connection the buffer gets, and why, and what the server is. :DBV opens
-- dbv on the buffer's connection, or on one picked from a list when it has
-- none (:DBV NAME, Tab completes).
-- :DBVConnect picks the buffer's connection the same way, for completion
-- (dbv lsp), :DBVRun and :DBVInfo; the file is not touched.
-- The connection is the one :DBVConnect picked, else dbv's when nvim runs
-- inside it (DBV_DB), else the file's "-- dbv: db=NAME", else the only one
-- configured.
local M = {}

-- byte offset of row (from 1), col (a byte, from 0) in lines joined by \n
local function offset(lines, row, col)
  local n = 0
  for i = 1, row - 1 do
    n = n + #lines[i] + 1
  end
  return n + col
end

-- the -at argument: the cursor's offset; a charwise selection's FROM:TO
-- when the range is '<,'>; a block's, appended to lines (the file keeps
-- its "-- dbv: db=" header); else whole lines
local function at(opts, lines)
  local visual = opts.range > 0 and vim.fn.histget(":", -1):match("^%s*'<%s*,%s*'>") ~= nil
  if visual and vim.fn.visualmode() == "\22" and vim.fn.exists("*getregion") == 1 then
    local block = vim.fn.getregion(vim.fn.getpos("'<"), vim.fn.getpos("'>"), { type = "\22" })
    table.insert(lines, "")
    local first = #lines + 1
    vim.list_extend(lines, block)
    return offset(lines, first, 0) .. ":" .. offset(lines, #lines, #lines[#lines])
  end
  if visual and vim.fn.visualmode() == "v" then
    local s, e = vim.fn.getpos("'<"), vim.fn.getpos("'>")
    local last = lines[e[2]] or ""
    local col = math.min(e[3], #last) -- the last character's first byte, from 1
    local stop = col - 1 -- selection=exclusive: '> is past the selection
    if col > 0 and vim.o.selection ~= "exclusive" then
      stop = col + vim.str_utf_end(last, col) -- through its last byte
    end
    return offset(lines, s[2], s[3] - 1) .. ":" .. offset(lines, e[2], math.max(stop, 0))
  end
  if opts.range == 0 then
    local cur = vim.api.nvim_win_get_cursor(0)
    return tostring(offset(lines, cur[1], cur[2]))
  end
  local last = lines[opts.line2] or ""
  return offset(lines, opts.line1, 0) .. ":" .. offset(lines, opts.line2, #last)
end

-- term runs cmd in a split below; done runs once it exits, and the split
-- closes when it exits with 0
local function term(cmd, done)
  vim.cmd("botright " .. math.floor(vim.o.lines / 2) .. "split")
  local win = vim.api.nvim_get_current_win()
  local buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_win_set_buf(win, buf)
  local job = {
    on_exit = function(_, code)
      vim.schedule(function()
        done()
        if code ~= 0 then
          return -- the error stays on screen; a key closes it
        end
        if vim.api.nvim_win_is_valid(win) then
          pcall(vim.api.nvim_win_close, win, true)
        end
        if vim.api.nvim_buf_is_valid(buf) then
          pcall(vim.api.nvim_buf_delete, buf, { force = true })
        end
      end)
    end,
  }
  if vim.fn.has("nvim-0.11") == 1 then
    job.term = true
    vim.fn.jobstart(cmd, job)
  else
    vim.fn.termopen(cmd, job)
  end
  vim.cmd("startinsert")
end

function M.run(opts)
  opts = opts or { range = 0 }
  local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false)
  local cmd = { "dbv", "show", "-at", at(opts, lines) }
  if vim.b.dbv_db then
    vim.list_extend(cmd, { "-db", vim.b.dbv_db })
  end
  local tmp = vim.fn.tempname() .. ".sql"
  vim.fn.writefile(lines, tmp) -- each line and a \n: the offsets above
  table.insert(cmd, tmp)
  term(cmd, function() os.remove(tmp) end)
end

-- titles are the configured connections' titles, from dbv dbs
local function titles()
  local r = vim.system({ "dbv", "dbs", "-json" }, { text = true }):wait()
  if r.code ~= 0 then
    return nil, vim.trim(r.stderr or "")
  end
  local list = {}
  for _, c in ipairs(vim.json.decode(r.stdout)) do
    table.insert(list, c.title)
  end
  return list
end

-- pick calls use with the connection named in opts, or picked from the list
local function pick(opts, use)
  if opts and opts.args ~= "" then
    use(opts.args)
    return
  end
  local list, err = titles()
  if not list then
    vim.notify(err, vim.log.levels.ERROR)
    return
  end
  vim.ui.select(list, { prompt = "dbv: connection" }, function(name)
    if name then
      use(name)
    end
  end)
end

-- header is the connection a "-- dbv: db=NAME" line names among the
-- file's first comment lines, as dbv's headerDB reads it
local function header(buf)
  for _, line in ipairs(vim.api.nvim_buf_get_lines(buf, 0, 50, false)) do
    line = vim.trim(line)
    if line ~= "" then
      local rest = line:match("^%-%-%s*(.*)$")
      if not rest then
        return nil
      end
      local name = rest:match("^dbv:.-db=(.*)$")
      if name then
        return vim.trim(name)
      end
    end
  end
end

-- M.open opens dbv on the connection named, else the buffer's (:DBVConnect,
-- the dbv nvim runs under, the file's header), else one picked from the
-- list, which the only one skips.
function M.open(opts)
  local open = function(name)
    local cmd = { "dbv", "-db", name }
    if (vim.env.DBV_CONFIG or "") ~= "" and (vim.env.DBV_DB or "") ~= "" then
      vim.list_extend(cmd, vim.split(vim.env.DBV_CONFIG, ":", { plain = true })) -- the names are in dbv's files
    end
    term(cmd, function() end)
  end
  local buf = vim.api.nvim_get_current_buf()
  local env = vim.env.DBV_DB ~= "" and vim.env.DBV_DB or nil
  local name = opts and opts.args ~= "" and opts.args or vim.b[buf].dbv_db or env or header(buf)
  if name then
    open(name)
    return
  end
  local list, err = titles()
  if not list then
    vim.notify(err, vim.log.levels.ERROR)
    return
  end
  if #list == 1 then
    open(list[1])
    return
  end
  vim.ui.select(list, { prompt = "dbv: connection" }, function(picked)
    if picked then
      open(picked)
    end
  end)
end

-- coc registers dbv.connect only once dbv lsp has started, so a
-- :DBVConnect right after opening the file retries for up to 10s
local function coc(uri, name, tries)
  vim.fn.CocActionAsync("runCommand", "dbv.connect", uri, name, function(err)
    if err == nil or err == vim.NIL then
      return
    end
    local missing = tostring(err):find("not found", 1, true) ~= nil
    if missing and tries > 0 then
      vim.defer_fn(function() coc(uri, name, tries - 1) end, 250)
      return
    end
    if missing then
      err = "dbv lsp did not start for this buffer; completion stays off (" .. tostring(err) .. ")"
    end
    vim.notify("dbv: " .. tostring(err), vim.log.levels.ERROR)
  end)
end

-- M.connect makes the connection named, or picked, this buffer's: for dbv
-- lsp (its dbv.connect command, through nvim's client or coc), :DBVRun and
-- :DBVInfo.
function M.connect(opts)
  local buf = vim.api.nvim_get_current_buf()
  pick(opts, function(name)
    vim.b[buf].dbv_db = name
    local uri = vim.uri_from_bufnr(buf)
    local params = { command = "dbv.connect", arguments = { uri, name } }
    local told = false
    for _, client in ipairs(vim.lsp.get_clients({ bufnr = buf, name = "dbv" })) do
      if vim.fn.has("nvim-0.11") == 1 then
        client:request("workspace/executeCommand", params, nil, buf)
      else
        client.request("workspace/executeCommand", params, nil, buf)
      end
      told = true
    end
    if not told and vim.fn.exists("*CocActionAsync") == 1 then
      coc(uri, name, 40)
      told = true
    end
    local say = "dbv: " .. name .. " for this buffer"
    if not told then
      say = say .. " (:DBVRun, :DBVInfo; no dbv lsp attached to complete with it)"
    end
    vim.notify(say)
  end)
end

-- a float with lines; q or <Esc> closes it
local function float(lines)
  local width = 20
  for _, l in ipairs(lines) do
    width = math.max(width, vim.fn.strdisplaywidth(l))
  end
  width = math.min(width, vim.o.columns - 4)
  local height = math.min(#lines, vim.o.lines - 4)
  local buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.api.nvim_open_win(buf, true, {
    relative = "editor", style = "minimal", border = "rounded", title = " dbv ",
    width = width, height = height,
    row = math.floor((vim.o.lines - height) / 2), col = math.floor((vim.o.columns - width) / 2),
  })
  for _, key in ipairs({ "q", "<Esc>" }) do
    vim.keymap.set("n", key, "<cmd>close<CR>", { buffer = buf, nowait = true })
  end
end

-- M.info shows the connection this buffer gets and the server behind it.
function M.info()
  local tmp = vim.fn.tempname() .. ".sql"
  vim.fn.writefile(vim.api.nvim_buf_get_lines(0, 0, -1, false), tmp) -- the header names the database
  local cmd = { "dbv", "info", tmp }
  if vim.b.dbv_db then
    cmd = { "dbv", "info", "-db", vim.b.dbv_db, tmp }
  end
  vim.system(cmd, { text = true }, function(r)
    vim.schedule(function()
      os.remove(tmp)
      local out = vim.split(vim.trim((r.stdout or "") .. (r.stderr or "")), "\n")
      float(out)
    end)
  end)
end

vim.api.nvim_create_user_command("DBVRun", M.run, { range = true })
vim.api.nvim_create_user_command("DBVInfo", M.info, {})
-- titles have spaces and nvim replaces only the last word (lead), so each
-- match is what follows the words already typed
local function complete(lead, line)
  local typed = line:match("^%s*%S+%s+(.*)$") or ""
  local out = {}
  for _, t in ipairs(titles() or {}) do
    if vim.startswith(t, typed) then
      table.insert(out, lead .. t:sub(#typed + 1))
    end
  end
  return out
end
vim.api.nvim_create_user_command("DBV", M.open, { nargs = "?", complete = complete })
vim.api.nvim_create_user_command("DBVConnect", M.connect, { nargs = "?", complete = complete })

return M
