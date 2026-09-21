import { expect, test } from './officialChromeTest';
import { webPassword, webUsername } from './synonGoWebCredentials';

for (const viewport of [{width:1440,height:1000},{width:1000,height:800},{width:390,height:844}]) {
  test.describe('scientific environments at ' + viewport.width, () => {
    test.use({viewport});
    test('loads the real catalog, filters software, and never installs on opening dialogs', async ({page}, info) => {
      await page.goto('/#/login');
      await page.getByRole('combobox').selectOption('zh-CN');
      await page.getByRole('textbox',{name:'用户名'}).fill(webUsername);
      await page.getByRole('textbox',{name:'密码'}).fill(webPassword);
      await page.getByRole('button',{name:'登录',exact:true}).click();
      await expect(page).toHaveURL(/#\/guid/);
      const initial = await (await page.request.get('/api/preferences/scientific-runtimes')).json();
      const mutations:string[]=[];
      page.on('request', request => {if(request.url().includes('/api/preferences/scientific-runtimes') && request.method() !== 'GET') mutations.push(request.method());});
      await page.goto('/#/settings/environments');
      const library=page.getByTestId('scientific-environments');
      await expect(library.locator('article')).toHaveCount(initial.options.length);
      await expect(library.getByRole('heading',{name:/科研环境/})).toBeVisible();
      const card=library.locator('[data-environment-id="autodock-vina"]');
      await card.locator('summary').click();
      await expect(card.locator('code').first()).toBeVisible();
      await library.getByRole('searchbox').fill('vina');
      await expect(library.locator('article')).toHaveCount(1);
      await library.getByRole('searchbox').fill('');
      await library.getByRole('combobox',{name:'科研分类'}).selectOption('omics');
      await expect(library.locator('article')).toHaveCount(2);
      await library.getByRole('combobox',{name:'科研分类'}).selectOption('all');
      await library.getByRole('button',{name:'管理预下载'}).click();
      await expect(page.getByRole('dialog').getByRole('checkbox')).toHaveCount(initial.options.length);
      await page.locator('.arco-modal-close-icon').click();
      const downloadable=library.getByRole('button',{name:'下载环境'}).first();
      await downloadable.click();
      await expect(page.getByRole('dialog').getByRole('button',{name:'确认下载'})).toBeVisible();
      await page.getByRole('dialog').getByRole('button',{name:'取消',exact:true}).click();
      expect(mutations).toEqual([]);
      await page.screenshot({path:info.outputPath('environments.png'),fullPage:true});
      const overflow=await library.evaluate(el => el.scrollWidth>el.clientWidth+1);
      expect(overflow).toBe(false);
      await page.reload();
      await expect(page.getByTestId('scientific-environments').locator('article')).toHaveCount(initial.options.length);
      const after=await (await page.request.get('/api/preferences/scientific-runtimes')).json();
      expect(after.options.map((item:{id:string;selected:boolean})=>[item.id,item.selected])).toEqual(initial.options.map((item:{id:string;selected:boolean})=>[item.id,item.selected]));
      await page.goto('/#/settings/storage');
      await expect(page.getByTestId('synon-storage-settings')).toBeVisible();
      await expect(page.locator('.storage-runtime-panel')).toHaveCount(0);
    });
  });
}
