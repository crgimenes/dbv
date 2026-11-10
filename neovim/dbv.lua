-- dbv.lua: run the SQL statement under the cursor, or the selection, and
-- see its rows in dbv's grid in a split below (dbv show -h). :DBVRun runs
-- the statement at the cursor; from visual mode (:'<,'>DBVRun) the
-- selection, a block (Ctrl-V) too; with a range of lines, those lines. :DBVInfo says which
-- connection the buffer gets, and why, and what the server is.
-- The connection is dbv's when nvim runs inside it (DBV_DB), else the
-- file's "-- dbv: db=NAME", else the only one configured.
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

function M.run(opts)
  opts = opts or { range = 0 }
  local lines = vim.api.nvim_buf_get_lines(0, 0, -1, false)
  local cmd = { "dbv", "show", "-at", at(opts, lines) }
  local tmp = vim.fn.tempname() .. ".sql"
  vim.fn.writefile(lines, tmp) -- each line and a \n: the offsets above
  table.insert(cmd, tmp)

  vim.cmd("botright " .. math.floor(vim.o.lines / 2) .. "split")
  local win = vim.api.nvim_get_current_win()
  local buf = vim.api.nvim_create_buf(false, true)
  vim.api.nvim_win_set_buf(win, buf)
  local job = {
    on_exit = function(_, code)
      vim.schedule(function()
        os.remove(tmp)
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
  vim.system({ "dbv", "info", tmp }, { text = true }, function(r)
    vim.schedule(function()
      os.remove(tmp)
      local out = vim.split(vim.trim((r.stdout or "") .. (r.stderr or "")), "\n")
      float(out)
    end)
  end)
end

vim.api.nvim_create_user_command("DBVRun", M.run, { range = true })
vim.api.nvim_create_user_command("DBVInfo", M.info, {})

return M
