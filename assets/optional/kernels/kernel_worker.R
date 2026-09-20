#!/usr/bin/env Rscript

.secret_vars <- Sys.getenv("OPERON_SECRET_VARS", "")
if (nchar(.secret_vars) > 0L) {
  for (.secret_key in strsplit(.secret_vars, ",", fixed = TRUE)[[1L]]) {
    Sys.unsetenv(.secret_key)
  }
  rm(.secret_key)
}
Sys.unsetenv("OPERON_SECRET_VARS")
rm(.secret_vars)

if (isFALSE(requireNamespace("jsonlite", quietly = TRUE))) {
  cat("{\"id\":\"startup\",\"stdout\":\"\",\"stderr\":\"\",\"error\":\"jsonlite package is not installed. Install it with: install.packages('jsonlite')\"}\n", file = stdout())
  flush(stdout())
  quit(status = 1)
}
suppressPackageStartupMessages(library(jsonlite))

.operon_startup_diagnostic <- Sys.getenv("OPERON_R_STARTUP_DIAGNOSTIC", "")
Sys.unsetenv("OPERON_R_STARTUP_DIAGNOSTIC")

.operon_oplog_max_bytes <- 8L * 1024L * 1024L
.operon_oplog_max_line_bytes <- 64L * 1024L
.operon_install_wrappers <- new.env(parent = emptyenv())
.operon_install_argument <- function(arguments, name) {
  value <- arguments[[name]]
  if (is.null(value) && length(arguments) > 0L) value <- arguments[[1L]]
  value
}
.operon_log_install <- function(operation, packages, result = "success", extra = list()) {
  tryCatch({
    path <- Sys.getenv("OPERON_R_OPLOG_PATH", "")
    if (!nzchar(path)) return(invisible())
    packages <- unique(as.character(packages))
    packages <- packages[!is.na(packages) & nzchar(packages)]
    record <- c(list(
      timestamp = format(Sys.time(), "%Y-%m-%dT%H:%M:%SZ", tz = "UTC"),
      operation = operation,
      packages = I(packages),
      result = result
    ), extra)
    line <- jsonlite::toJSON(record, auto_unbox = TRUE, null = "null")
	line_bytes <- nchar(enc2utf8(line), type = "bytes") + 1L
	if (is.na(line_bytes) || line_bytes > .operon_oplog_max_line_bytes) return(invisible())
	current_size <- tryCatch(file.info(path)$size[[1L]], error = function(condition) 0)
	if (is.na(current_size)) current_size <- 0
	if (current_size + line_bytes > .operon_oplog_max_bytes) return(invisible())
    cat(line, "\n", file = path, append = TRUE, sep = "")
  }, error = function(condition) NULL)
  invisible()
}

.operon_packages_installed <- function(packages) {
  packages <- unique(as.character(packages))
  packages <- packages[!is.na(packages) & nzchar(packages)]
  if (length(packages) == 0L) return(FALSE)
  installed <- tryCatch(rownames(utils::installed.packages()), error = function(condition) character())
  all(packages %in% installed)
}

.operon_install_wrapper <- function(key, namespace, name, operation, package_name) {
  if (exists(key, envir = .operon_install_wrappers, inherits = FALSE)) return(invisible())
  target <- tryCatch(asNamespace(namespace), error = function(condition) NULL)
  if (is.null(target) || !exists(name, envir = target, inherits = FALSE)) return(invisible())
  original <- get(name, envir = target, inherits = FALSE)
  wrapper <- function(...) {
    arguments <- list(...)
    requested <- package_name(arguments)
    result <- "error"
    extra <- list()
    if (identical(operation, "r_github_install")) {
      github_ref <- as.character(requested)
      extra$github_ref <- if (length(github_ref) == 1L) github_ref[[1L]] else I(github_ref)
    }
    on.exit(.operon_log_install(operation, requested, result, extra), add = TRUE)
    value <- original(...)
    verified <- requested
    if (identical(operation, "r_github_install")) {
      verified <- sub("@.*$", "", basename(as.character(requested)))
    }
    if (.operon_packages_installed(verified)) result <- "success"
    value
  }
  was_locked <- bindingIsLocked(name, target)
  global_exists <- exists(name, envir = .GlobalEnv, inherits = FALSE)
  global_original <- if (global_exists) get(name, envir = .GlobalEnv, inherits = FALSE) else NULL
  installed <- tryCatch({
    if (was_locked) unlockBinding(name, target)
    assign(name, wrapper, envir = target)
    if (was_locked) lockBinding(name, target)
    assign(name, wrapper, envir = .GlobalEnv)
    TRUE
  }, error = function(condition) {
    tryCatch({
      if (bindingIsLocked(name, target)) unlockBinding(name, target)
      assign(name, original, envir = target)
      if (was_locked) lockBinding(name, target)
      if (global_exists) {
        assign(name, global_original, envir = .GlobalEnv)
      } else if (exists(name, envir = .GlobalEnv, inherits = FALSE)) {
        rm(list = name, envir = .GlobalEnv)
      }
    }, error = function(rollback_condition) NULL)
    FALSE
  })
  if (isTRUE(installed)) assign(key, TRUE, envir = .operon_install_wrappers)
  invisible()
}

.operon_wrap_cran <- function() {
  .operon_install_wrapper(
    "utils::install.packages", "utils", "install.packages", "r_cran_install",
    function(arguments) .operon_install_argument(arguments, "pkgs")
  )
}
.operon_wrap_bioc <- function() {
  .operon_install_wrapper(
    "BiocManager::install", "BiocManager", "install", "r_bioc_install",
    function(arguments) .operon_install_argument(arguments, "pkgs")
  )
}
.operon_wrap_github <- function() {
  .operon_install_wrapper(
    "remotes::install_github", "remotes", "install_github", "r_github_install",
    function(arguments) .operon_install_argument(arguments, "repo")
  )
}

.operon_wrap_cran()
setHook(packageEvent("BiocManager", "onLoad"), function(...) .operon_wrap_bioc(), action = "append")
setHook(packageEvent("remotes", "onLoad"), function(...) .operon_wrap_github(), action = "append")
if ("BiocManager" %in% loadedNamespaces()) .operon_wrap_bioc()
if ("remotes" %in% loadedNamespaces()) .operon_wrap_github()

local({
  writable_roots <- Filter(
    nzchar,
    strsplit(Sys.getenv("OPERON_WRITABLE_ROOTS", ""), ":", fixed = TRUE)[[1L]]
  )
  dlopen_exempt <- Filter(
    nzchar,
    strsplit(Sys.getenv("OPERON_DLOPEN_EXEMPT", ""), ":", fixed = TRUE)[[1L]]
  )
  Sys.unsetenv("OPERON_WRITABLE_ROOTS")
  Sys.unsetenv("OPERON_DLOPEN_EXEMPT")
  if (length(writable_roots) == 0L) return(invisible())
  writable_roots <- sub("/+$", "", writable_roots)
  dlopen_exempt <- sub("/+$", "", dlopen_exempt)
  original_dyn_load <- base::dyn.load
  guarded_dyn_load <- function(path, ...) {
    resolved <- tryCatch(normalizePath(path, mustWork = FALSE), error = function(condition) path)
    for (root in dlopen_exempt) {
      if (identical(resolved, root) || startsWith(resolved, paste0(root, "/"))) {
        return(original_dyn_load(path, ...))
      }
    }
    for (root in writable_roots) {
      if (identical(resolved, root) || startsWith(resolved, paste0(root, "/"))) {
        stop(sprintf("Refusing to dyn.load shared library from writable path: %s", resolved), call. = FALSE)
      }
    }
    original_dyn_load(path, ...)
  }
  tryCatch(unlockBinding("dyn.load", baseenv()), error = function(condition) NULL)
  tryCatch(assign("dyn.load", guarded_dyn_load, envir = baseenv()), error = function(condition) NULL)
  tryCatch(lockBinding("dyn.load", baseenv()), error = function(condition) NULL)
})

MAX_OUTPUT_SIZE <- 1024L * 1024L
MAX_STREAM_BYTES <- 10L * 1024L * 1024L
session_env <- new.env(parent = globalenv())
options(warn = 1, max.print = 10000)

local({
  quit_message <- paste0(
    "q()/quit() is disabled here — this terminal shares the agent's ",
    "live kernel. Kernels are stopped from the UI, not from code."
  )
  guarded_quit <- function(save = "default", status = 0, runLast = TRUE) {
    normalized_status <- tryCatch(
      suppressWarnings(as.integer(status)[1L]),
      error = function(condition) NA_integer_
    )
    if (length(normalized_status) == 0L) normalized_status <- NA_integer_
    if (!is.na(normalized_status) && normalized_status != 0L) {
      stop(sprintf(paste0(
        "quit(status = %d) blocked — this kernel stays alive across ",
        "cells (it shares the agent's live session). Signal failure ",
        "with stop() or a non-zero return value instead."
      ), normalized_status), call. = FALSE)
    }
    stop(quit_message, call. = FALSE)
  }
  for (name in c("q", "quit")) {
    unlockBinding(name, baseenv())
    assign(name, guarded_quit, envir = baseenv())
    lockBinding(name, baseenv())
    assign(name, guarded_quit, envir = session_env)
  }
})

.active_stream_flush <- NULL
local({
  make_streaming_writer <- function(name) {
    original <- get(name, envir = baseenv())
    function(...) {
      result <- original(...)
      if (is.function(.active_stream_flush)) {
        tryCatch(.active_stream_flush(), error = function(condition) NULL)
      }
      invisible(result)
    }
  }
  for (name in c("print", "cat", "writeLines")) {
    assign(name, make_streaming_writer(name), envir = session_env)
  }
})

new_stream_flush <- function(request_id, output_path, output_connection, protocol_connection) {
  state <- new.env(parent = emptyenv())
  state$offset <- 0
  state$emitted <- 0
  state$pending <- raw(0)
  state$truncated <- FALSE
  function() {
    flush(output_connection)
    size <- file.info(output_path)$size
    if (is.na(size) || size <= state$offset || state$truncated) return(invisible())
    remaining <- MAX_STREAM_BYTES - state$emitted
    if (remaining <= 0L) return(invisible())
    connection <- file(output_path, open = "rb")
    on.exit(close(connection), add = TRUE)
    seek(connection, where = state$offset, origin = "start")
    available <- size - state$offset
    take <- min(available, remaining)
    chunk <- readBin(connection, what = "raw", n = take)
    state$offset <- state$offset + length(chunk)
    combined <- c(state$pending, chunk)
    state$pending <- raw(0)
    text <- NA_character_
    while (length(combined) > 0L) {
      candidate <- tryCatch(rawToChar(combined), error = function(condition) NA_character_)
      if (!is.na(candidate) && !is.na(nchar(candidate, type = "chars", allowNA = TRUE))) {
        text <- enc2utf8(candidate)
        break
      }
      state$pending <- c(combined[length(combined)], state$pending)
      combined <- combined[-length(combined)]
      if (length(state$pending) > 4L) return(invisible())
    }
    state$emitted <- state$emitted + length(chunk)
    if (size > state$offset && state$emitted >= MAX_STREAM_BYTES) {
      text <- paste0(text, "\n…(live stream truncated at 10 MB; full output in tool_result)\n")
      state$truncated <- TRUE
    }
    if (is.na(text) || text == "") return(invisible())
    message <- toJSON(list(type = "stdout_chunk", id = request_id, data = text), auto_unbox = TRUE)
    writeLines(message, con = protocol_connection)
    flush(protocol_connection)
    invisible()
  }
}

read_capped_output <- function(path, limit = MAX_OUTPUT_SIZE) {
  connection <- file(path, open = "rb")
  on.exit(close(connection), add = TRUE)
  bytes <- readBin(connection, what = "raw", n = (limit * 4L) + 1L)
  if (length(bytes) == 0L) return("")
  while (length(bytes) > 0L) {
    value <- tryCatch(rawToChar(bytes), error = function(condition) NA_character_)
    if (!is.na(value) && !is.na(nchar(value, type = "chars", allowNA = TRUE))) break
    bytes <- bytes[-length(bytes)]
  }
  if (length(bytes) == 0L) return("")
  value <- enc2utf8(value)
  characters <- nchar(value, type = "chars")
  if (characters > limit) {
    value <- paste0(
      substr(value, 1L, limit),
      sprintf("\n... (truncated, %d bytes omitted)", characters - limit)
    )
  }
  value
}

condition_line <- function(condition) {
  reference <- attr(condition, "srcref")
  if (!is.null(reference) && length(reference) >= 1L) {
    return(as.integer(reference[[1L]]))
  }
  NULL
}

expression_line <- function(parsed, index) {
  if (is.null(parsed) || index <= 0L) return(NULL)
  tryCatch(
    as.integer(getSrcLocation(getSrcref(parsed)[[index]], "line", first = TRUE)),
    error = function(condition) NULL
  )
}

# The manager retains the same taint outside this mutable interpreter. A prior
# arbitrary cell never becomes trusted by changing this worker-local value.
.observation_tainted <- FALSE
.observation_bindings <- list(
  "r.directory"=list(name="getwd", value=base::getwd),
  "r.process"=list(name="Sys.getpid", value=base::Sys.getpid),
  "r.system"=list(name="Sys.info", value=base::Sys.info)
)
.observation_print <- base::print
observation_binding_matches <- function(name, expected, scope) {
  while (!identical(scope, emptyenv())) {
    if (exists(name, envir=scope, inherits=FALSE)) {
      # Do not force a promise or active binding while trying to prove it.
      # These capabilities must resolve directly to the captured base binding.
      if (!identical(scope, baseenv()) || bindingIsActive(name, scope)) return(FALSE)
      return(identical(get(name, envir=scope, inherits=FALSE), expected))
    }
    scope <- parent.env(scope)
  }
  FALSE
}
observation_refusal <- function(request) {
  proof <- request$observation
  if (is.null(proof)) {
    .observation_tainted <<- TRUE
    return(NULL)
  }
  if (isTRUE(.observation_tainted)) return("runtime_binding_provenance_unproved")
  if (!identical(proof$schema, "synon.execution-observation.v1") ||
      !identical(proof$language, "r")) return("diagnostic_contract_invalid")
  # The host checked the raw code digest under its execute lock immediately
  # before sending this request. The worker parses that unchanged code below.
  if (!is.character(proof$source_sha256) || length(proof$source_sha256) != 1L ||
      !grepl("^[a-f0-9]{64}$", proof$source_sha256) ||
      !identical(proof$source_sha256, request$observation_code_sha256)) return("diagnostic_source_binding_mismatch")
  operations <- proof$operations
  if (!is.list(operations) || length(operations) == 0L || length(operations) > 256L) return("diagnostic_operation_unproved")
  for (operation in operations) {
    if (!is.character(operation) || length(operation) != 1L) return("diagnostic_operation_unproved")
    if (identical(operation, "r.literal")) next
    binding <- .observation_bindings[[operation]]
    if (is.null(binding) || !observation_binding_matches(binding$name, binding$value, session_env))
      return("diagnostic_binding_unproved")
  }
  if (!observation_binding_matches("print", .observation_print, globalenv()))
    return("diagnostic_implicit_binding_unproved")
  NULL
}

evaluate_request <- function(request, protocol_connection) {
  request_id <- if (is.character(request$id) && length(request$id) == 1L) request$id else ""
  code <- if (is.character(request$code) && length(request$code) == 1L) request$code else ""
  workspace <- if (is.character(request$workspace_dir) && length(request$workspace_dir) == 1L) request$workspace_dir else ""
  working_directory <- if (is.character(request$working_dir) && length(request$working_dir) == 1L) request$working_dir else ""
  if (request_id == "" || code == "") {
    return(list(
      id = request_id,
      stdout = "",
      stderr = "",
      error = "kernel request requires id and code",
      interrupted = FALSE,
      trace = list(error_lineno = NULL, error_call = NULL),
      usage = list(wall_s = 0, cpu_s = 0, peak_rss_kb = NULL)
    ))
  }

  refusal <- observation_refusal(request)
  if (!is.null(refusal)) {
    return(list(id=request_id, stdout="", stderr="", error=NULL, interrupted=FALSE,
      trace=list(error_lineno=NULL, error_call=NULL), usage=list(),
      preflight=list(schema="synon.execution-observation.v1", ok=FALSE,
        status="implementation_selection_required", executed=FALSE, decision_required=TRUE,
        reason=refusal,
        message="Diagnostic runtime bindings are unproved; scientific implementation selection remains required.",
        recovery="Inspect the existing runtime and complete the pending implementation choice. Do not retry unchanged or reset the session implicitly.")))
  }

  stdout_path <- tempfile(pattern = "synon-r-stdout-", tmpdir = getwd())
  stderr_path <- tempfile(pattern = "synon-r-stderr-", tmpdir = getwd())
  stdout_connection <- file(stdout_path, open = "wt", encoding = "UTF-8")
  stderr_connection <- file(stderr_path, open = "wt", encoding = "UTF-8")
  on.exit(unlink(c(stdout_path, stderr_path), force = TRUE), add = TRUE)

  error_text <- NULL
  error_call <- NULL
  error_line <- NULL
  interrupted <- FALSE
  parsed <- NULL
  expression_index <- 0L
  started <- proc.time()

  output_sunk <- FALSE
  message_sunk <- FALSE
  tryCatch(
    {
      if (working_directory != "") {
        setwd(working_directory)
      }
      started_message <- toJSON(list(type = "execution_started", id = request_id), auto_unbox = TRUE)
      writeLines(started_message, con = protocol_connection)
      flush(protocol_connection)
      sink(stdout_connection, type = "output")
      output_sunk <- TRUE
      sink(stderr_connection, type = "message")
      message_sunk <- TRUE
      .active_stream_flush <<- new_stream_flush(
        request_id, stdout_path, stdout_connection, protocol_connection
      )
      parsed <- parse(text = code, keep.source = TRUE)
      for (index in seq_along(parsed)) {
        expression_index <- index
        expression <- parsed[[index]]
        visible <- withVisible(eval(expression, envir = session_env))
        if (isTRUE(visible$visible)) {
          print(visible$value)
        }
		.active_stream_flush()
      }
    },
    interrupt = function(condition) {
      interrupted <<- TRUE
	  error_text <<- "Interrupted"
      error_call <<- paste(deparse(conditionCall(condition)), collapse = " ")
	  error_line <<- expression_line(parsed, expression_index)
	  if (is.null(error_line)) error_line <<- condition_line(condition)
    },
    error = function(condition) {
      error_text <<- conditionMessage(condition)
      call <- conditionCall(condition)
      if (!is.null(call)) {
        error_call <<- paste(deparse(call), collapse = " ")
      }
	  error_line <<- expression_line(parsed, expression_index)
	  if (is.null(error_line)) error_line <<- condition_line(condition)
    }
  )
	if (is.function(.active_stream_flush)) {
	  tryCatch(.active_stream_flush(), error = function(condition) NULL)
	}
	.active_stream_flush <<- NULL
  if (message_sunk) {
    sink(type = "message")
  }
  if (output_sunk) {
    sink(type = "output")
  }
  close(stderr_connection)
  close(stdout_connection)

  elapsed <- proc.time() - started
	stderr_value <- read_capped_output(stderr_path)
	if (nzchar(.operon_startup_diagnostic)) {
	  stderr_value <- if (nzchar(stderr_value)) {
	    paste0(.operon_startup_diagnostic, "\n", stderr_value)
	  } else {
	    .operon_startup_diagnostic
	  }
	  .operon_startup_diagnostic <<- ""
	}
  list(
    id = request_id,
    stdout = read_capped_output(stdout_path),
	stderr = stderr_value,
    error = error_text,
    interrupted = interrupted,
    trace = list(error_lineno = error_line, error_call = error_call),
    usage = list(
      wall_s = unname(elapsed[["elapsed"]]),
      cpu_s = unname(elapsed[["user.self"]] + elapsed[["sys.self"]]),
      peak_rss_kb = NULL
    )
  )
}

input_connection <- file("stdin", open = "rt", encoding = "UTF-8")
protocol_connection <- stdout()

repeat {
  line <- readLines(input_connection, n = 1L, warn = FALSE)
  if (length(line) == 0L) {
    break
  }
  response <- tryCatch(
    evaluate_request(fromJSON(line, simplifyVector = FALSE), protocol_connection),
    error = function(condition) list(
      id = "",
      stdout = "",
      stderr = "",
      error = paste("invalid kernel request:", conditionMessage(condition)),
      interrupted = FALSE,
      trace = list(error_lineno = NULL, error_call = NULL),
      usage = list(wall_s = 0, cpu_s = 0, peak_rss_kb = NULL)
    )
  )
  encoded <- toJSON(response, auto_unbox = TRUE, null = "null", na = "null")
  writeLines(encoded, con = protocol_connection)
  flush(protocol_connection)
}
