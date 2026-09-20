# Native AST coverage and runtime identities share this operation registry.
# This is an effect contract, not a sandbox or permission boundary.
$observationModules = [System.IO.Path]::Combine($PSHOME, 'Modules')
$env:PSModulePath = $observationModules
$PSModuleAutoLoadingPreference = 'None'
foreach ($observationModule in @('Microsoft.PowerShell.Utility', 'Microsoft.PowerShell.Management')) {
    $observationManifest = [System.IO.Path]::Combine($observationModules, $observationModule, $observationModule + '.psd1')
    Microsoft.PowerShell.Core\Import-Module -Name $observationManifest -ErrorAction Stop
}

function Get-SynonObservation([string]$Source) {
    $tokens = $null
    $errors = $null
    $tree = [System.Management.Automation.Language.Parser]::ParseInput($Source, [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0 -or $null -ne $tree.ParamBlock -or $null -ne $tree.BeginBlock -or
        $null -ne $tree.ProcessBlock -or $null -ne $tree.DynamicParamBlock -or
        $tree.Attributes.Count -ne 0 -or $tree.UsingStatements.Count -ne 0 -or
        $null -eq $tree.EndBlock -or $tree.EndBlock.Traps.Count -ne 0 -or
        $tree.EndBlock.Statements.Count -eq 0 -or $tree.EndBlock.Statements.Count -gt 256) { return $null }
    # PowerShell 7 adds a clean block. Reject it when that AST property exists.
    if ($null -ne $tree.PSObject.Properties['CleanBlock'] -and $null -ne $tree.CleanBlock) { return $null }
    $operations = [System.Collections.Generic.HashSet[string]]::new()
    foreach ($statement in $tree.EndBlock.Statements) {
        if ($statement -isnot [System.Management.Automation.Language.PipelineAst] -or
            $statement.PipelineElements.Count -ne 1 -or
            ($null -ne $statement.PSObject.Properties['Background'] -and $statement.Background)) { return $null }
        $element = $statement.PipelineElements[0]
        if ($element.Redirections.Count -ne 0) { return $null }
        if ($element -is [System.Management.Automation.Language.CommandExpressionAst]) {
            if ($element.Expression -isnot [System.Management.Automation.Language.ConstantExpressionAst]) { return $null }
            [void]$operations.Add('powershell.literal')
            continue
        }
        if ($element -isnot [System.Management.Automation.Language.CommandAst] -or
            $element.InvocationOperator -ne [System.Management.Automation.Language.TokenKind]::Unknown -or
            $element.CommandElements.Count -eq 0 -or $element.CommandElements.Count -gt 256) { return $null }
        foreach ($operand in $element.CommandElements) {
            if ($operand -isnot [System.Management.Automation.Language.ConstantExpressionAst]) { return $null }
        }
        $name = $element.GetCommandName()
        switch ($name) {
            'Get-Location' {
                if ($element.CommandElements.Count -ne 1) { return $null }
                [void]$operations.Add('powershell.directory')
            }
            'Write-Output' { [void]$operations.Add('powershell.output') }
            default { return $null }
        }
    }
    return ,([string[]]($operations | Microsoft.PowerShell.Utility\Sort-Object))
}

function Assert-SynonObservationBindings([string[]]$Operations) {
    foreach ($operation in $Operations) {
        if ($operation -eq 'powershell.literal') { continue }
        $name = $null
        switch ($operation) {
            'powershell.directory' { $name = 'Get-Location'; $type = 'Microsoft.PowerShell.Commands.GetLocationCommand'; $module = 'Microsoft.PowerShell.Management' }
            'powershell.output' { $name = 'Write-Output'; $type = 'Microsoft.PowerShell.Commands.WriteOutputCommand'; $module = 'Microsoft.PowerShell.Utility' }
            default { throw 'diagnostic_operation_unproved' }
        }
        # Resolve the command the user source would actually invoke. A function
        # or alias shadow is a refusal, never silently replaced with a cmdlet.
        $binding = Microsoft.PowerShell.Core\Get-Command -Name $name -ErrorAction Stop
        if ($binding -isnot [System.Management.Automation.CmdletInfo] -or
            $binding.ImplementingType.FullName -cne $type -or $binding.ModuleName -cne $module) {
            throw 'diagnostic_binding_unproved'
        }
    }
}
