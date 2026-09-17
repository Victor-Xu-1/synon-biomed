import React from 'react';
import { useTranslation } from 'react-i18next';
import type { SynonBiomedKernel, SynonBiomedMachineMetrics } from '@/renderer/services/synonBiomedNotebook';
import { formatCoresFromPercent, formatMemory, sumNullable } from './computeRuntimeModel';

type ComputeMachineHeaderProps = {
  kernels: SynonBiomedKernel[];
  machine: SynonBiomedMachineMetrics | null;
  stale: boolean;
};

const ComputeMachineHeader: React.FC<ComputeMachineHeaderProps> = ({ kernels, machine, stale }) => {
  const { t } = useTranslation();
  const totalMemory = machine?.totalMemoryBytes ?? null;
  const availableMemory = machine?.availableMemoryBytes ?? null;
  const kernelRss = machine?.kernelRssBytes ?? sumNullable(kernels.map((kernel) => kernel.rssBytes)) ?? 0;
  const kernelCpu = machine?.kernelCpuPct ?? sumNullable(kernels.map((kernel) => kernel.cpuPct));
  const totalCpu = machine?.cpuPct ?? null;
  const hostCores = machine?.hostCores ?? machine?.cores ?? null;
  const usedMemory = totalMemory != null && availableMemory != null ? Math.max(0, totalMemory - availableMemory) : null;
  const memoryKernelFraction = totalMemory && totalMemory > 0 ? kernelRss / totalMemory : 0;
  const memoryOtherFraction =
    totalMemory && totalMemory > 0 && usedMemory != null ? Math.max(0, usedMemory - kernelRss) / totalMemory : 0;
  const cpuKernelFraction = hostCores && hostCores > 0 && kernelCpu != null ? kernelCpu / (hostCores * 100) : 0;
  const cpuOtherFraction =
    hostCores && hostCores > 0 && totalCpu != null ? Math.max(0, totalCpu - (kernelCpu ?? 0)) / (hostCores * 100) : 0;
  const running = kernels.filter((kernel) => kernel.busy || kernel.starting).length;
  const sampledAt = machine?.sampledAt ? Date.parse(machine.sampledAt) : Number.NaN;

  return (
    <header className='sticky top-0 z-10 border-b border-solid border-[var(--color-border-2)] bg-1 px-16px pb-14px pt-12px'>
      <div className='synon-compute-header-grid'>
        <HeaderMetric
          value={formatMemory(kernelRss)}
          detail={
            stale && machine?.sampledAt
              ? t('conversation.synonRuntime.computeRuntime.memoryStale', {
                  age: `${Math.max(0, Math.round((Date.now() - sampledAt) / 1000))}s ago`,
                })
              : totalMemory != null && availableMemory != null
                ? t('conversation.synonRuntime.computeRuntime.memoryAvailable', {
                    free: formatMemory(availableMemory),
                    total: formatMemory(totalMemory),
                  })
                : t('conversation.synonRuntime.computeRuntime.memoryLabel')
          }
          meterLabel={t('conversation.synonRuntime.computeRuntime.memoryMeter')}
          kernelFraction={memoryKernelFraction}
          otherFraction={memoryOtherFraction}
          testId='synon-biomed-compute-header-rss'
        />
        <HeaderMetric
          value={formatCoresFromPercent(kernelCpu)}
          detail={
            totalCpu != null && hostCores != null
              ? t('conversation.synonRuntime.computeRuntime.cpuBusy', {
                  busy: (totalCpu / 100).toFixed(1),
                  cores: hostCores.toFixed(1),
                })
              : t('conversation.synonRuntime.computeRuntime.cpuMeasuring')
          }
          meterLabel={t('conversation.synonRuntime.computeRuntime.cpuMeter')}
          kernelFraction={cpuKernelFraction}
          otherFraction={cpuOtherFraction}
          testId='synon-biomed-compute-header-cpu'
        />
        <div className='synon-compute-header-total shrink-0'>
          <div className='text-18px font-500 tabular-nums leading-tight text-t-primary'>{kernels.length}</div>
          <div className='mt-1px whitespace-nowrap text-11px text-t-tertiary'>
            {t('conversation.synonRuntime.computeRuntime.machineKernelSummary', { running })}
          </div>
          {machine?.diskAvailableBytes != null && (
            <div className='mt-1px whitespace-nowrap text-11px tabular-nums text-t-tertiary'>
              {t('conversation.synonRuntime.computeRuntime.diskFree', {
                free: formatMemory(machine.diskAvailableBytes),
                total: formatMemory(machine.diskTotalBytes),
              })}
            </div>
          )}
        </div>
      </div>
    </header>
  );
};

const HeaderMetric: React.FC<{
  value: string;
  detail: React.ReactNode;
  meterLabel: string;
  kernelFraction: number;
  otherFraction: number;
  testId: string;
}> = ({ value, detail, meterLabel, kernelFraction, otherFraction, testId }) => (
  <div className='synon-compute-header-metric'>
    <div className='synon-compute-header-count'>
      <span
        className='whitespace-nowrap text-18px font-500 tabular-nums leading-tight text-t-primary'
        data-testid={testId}
      >
        {value}
      </span>
      <span className='text-11px text-t-tertiary'>{detail}</span>
    </div>
    <div className='mt-6px'>
      <ResourceMeter label={meterLabel} kernelFraction={kernelFraction} otherFraction={otherFraction} />
    </div>
  </div>
);

const ResourceMeter: React.FC<{
  label: string;
  kernelFraction: number;
  otherFraction: number;
}> = ({ label, kernelFraction, otherFraction }) => {
  const kernel = Math.max(0, Math.min(1, kernelFraction));
  const other = Math.max(0, Math.min(1 - kernel, otherFraction));
  return (
    <div
      className='synon-compute-meter'
      role='meter'
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round((kernel + other) * 100)}
      aria-label={label}
    >
      <div className='synon-compute-meter__segments'>
        <div className='synon-compute-meter__kernel' style={{ width: `${kernel * 100}%` }} />
        <div className='synon-compute-meter__other' style={{ width: `${other * 100}%` }} />
      </div>
    </div>
  );
};

export default ComputeMachineHeader;
