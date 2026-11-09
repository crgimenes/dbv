-- dbv.lua: run the SQL statement under the cursor, or the selection, and
-- see its rows in dbv's grid in a split below (dbv show -h). In .sql
-- buffers: <C-CR> or <leader>t, normal or visual; anywhere: :DBVRun.
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
-- (visual, from the mapping); else whole lines, of a range or a selection
local function at(opts, lines)
  if opts.visual and vim.fn.visualmode() == "v" then
    local s, e = vim.fn.getpos("'<"), vim.fn.getpos("'>")
    local last = lines[e[2]] or ""
    local col = math.min(e[3], #last) -- the last character's first byte, from 1
    local stop = col - 1 -- selection=exclusive: '> is past the selection
    if col > 0 and vim.o.selection ~= "exclusive" then
      stop = col + vim.str_utf_end(last, col) -- through its last byte
    end
    return offset(lines, s[2], s[3] - 1) .. ":" .. offset(lines, e[2], math.max(stop, 0))
  end
  if opts.visual then
    opts.line1, opts.line2 = vim.fn.line("'<"), vim.fn.line("'>")
  elseif opts.range == 0 then
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

vim.api.nvim_create_user_command("DBVRun", M.run, { range = true })

vim.api.nvim_create_autocmd("FileType", {
  pattern = "sql",
  group = vim.api.nvim_create_augroup("dbv", { clear = true }),
  callback = function(ev)
    for _, key in ipairs({ "<C-CR>", "<leader>t" }) do
      vim.keymap.set("n", key, "<cmd>DBVRun<CR>", { buffer = ev.buf, desc = "dbv: run the statement" })
      vim.keymap.set("x", key, function()
        vim.cmd("normal! \27") -- leave visual mode: it sets '< and '>
        M.run({ visual = true })
      end, { buffer = ev.buf, desc = "dbv: run the selection" })
    end
  end,
})

return M
