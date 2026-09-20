import React from 'react';
import ReactMarkdown from 'react-markdown';
import remarkBreaks from 'remark-breaks';
import remarkGfm from 'remark-gfm';

const SKILL_REMARK_PLUGINS = [remarkGfm, remarkBreaks];

export function SkillFilePreview({ content, path }: { content: string; path: string }) {
  if (!/\.md$/i.test(path)) {
    return (
      <pre className='m-0 whitespace-pre-wrap break-words font-mono text-12px' data-testid='skill-source-preview'>
        {content}
      </pre>
    );
  }
  return (
    <div className='skill-markdown-preview break-words text-t-primary' data-testid='skill-markdown'>
      <ReactMarkdown
        skipHtml
        remarkPlugins={SKILL_REMARK_PLUGINS}
        components={{
          h1: ({ children }) => <h1 className='mb-14px mt-0 text-22px font-semibold leading-30px'>{children}</h1>,
          h2: ({ children }) => <h2 className='mb-10px mt-20px text-17px font-semibold leading-25px'>{children}</h2>,
          h3: ({ children }) => <h3 className='mb-8px mt-16px text-15px font-semibold leading-23px'>{children}</h3>,
          p: ({ children }) => <p className='my-10px leading-22px'>{children}</p>,
          ul: ({ children }) => <ul className='my-10px pl-22px leading-22px'>{children}</ul>,
          ol: ({ children }) => <ol className='my-10px pl-22px leading-22px'>{children}</ol>,
          li: ({ children }) => <li className='my-4px'>{children}</li>,
          blockquote: ({ children }) => (
            <blockquote className='my-12px border-l-2 border-arco-3 pl-12px text-t-secondary'>{children}</blockquote>
          ),
          a: ({ children, href }) => (
            <a href={href} target='_blank' rel='noreferrer' className='break-all text-link-6 hover:underline'>
              {children}
            </a>
          ),
          code: ({ children, className }) =>
            className ? (
              <code className={`${className} font-mono text-12px`}>{children}</code>
            ) : (
              <code className='rd-4px bg-fill-2 px-5px py-1px font-mono text-12px'>{children}</code>
            ),
          pre: ({ children }) => (
            <pre className='my-12px max-w-full overflow-auto rd-6px bg-fill-2 px-12px py-10px font-mono text-12px leading-20px'>
              {children}
            </pre>
          ),
          table: ({ children }) => (
            <div className='my-12px max-w-full overflow-x-auto'>
              <table className='w-full border-collapse border border-arco-2 text-12px'>{children}</table>
            </div>
          ),
          th: ({ children }) => (
            <th className='border border-arco-2 bg-fill-1 px-8px py-6px text-left font-medium'>{children}</th>
          ),
          td: ({ children }) => <td className='border border-arco-2 px-8px py-6px align-top'>{children}</td>,
          hr: () => <hr className='my-18px border-0 border-t border-arco-2' />,
        }}
      >
        {stripSkillFrontmatter(content)}
      </ReactMarkdown>
    </div>
  );
}

function stripSkillFrontmatter(content: string): string {
  const normalized = content.replace(/^\uFEFF/, '').trimStart();
  return normalized.replace(/^---[ \t]*\r?\n[\s\S]*?\r?\n---[ \t]*(?:\r?\n|$)/, '').trimStart();
}
